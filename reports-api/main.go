package main

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type config struct {
	ListenAddress      string
	ClickHouseURL      string
	ClickHouseUser     string
	ClickHousePassword string
	KeycloakIssuer     string
	KeycloakJWKSURL    string
	ExpectedAudience   string
	S3Endpoint         string
	S3AccessKey        string
	S3SecretKey        string
	S3Bucket           string
	S3Secure           bool
	CDNBaseURL         string
	ObjectKeySecret    string
}

type app struct {
	cfg          config
	httpClient   *http.Client
	keys         *keyCache
	objects      *objectStore
	generationMu sync.Mutex
}

type etlWatermark struct {
	ProcessedUntil time.Time
	UpdatedAt      time.Time
}

type tokenHeader struct {
	Algorithm string `json:"alg"`
	KeyID     string `json:"kid"`
}

type tokenClaims struct {
	Issuer            string          `json:"iss"`
	Subject           string          `json:"sub"`
	PreferredUsername string          `json:"preferred_username"`
	Audience          json.RawMessage `json:"aud"`
	ExpiresAt         int64           `json:"exp"`
}

type jwk struct {
	KeyID     string `json:"kid"`
	KeyType   string `json:"kty"`
	Algorithm string `json:"alg"`
	Modulus   string `json:"n"`
	Exponent  string `json:"e"`
}

type jwksResponse struct {
	Keys []jwk `json:"keys"`
}

type keyCache struct {
	url        string
	client     *http.Client
	mu         sync.Mutex
	keys       map[string]*rsa.PublicKey
	lastUpdate time.Time
}

func main() {
	cfg := config{
		ListenAddress:      envOrDefault("LISTEN_ADDRESS", ":8081"),
		ClickHouseURL:      envOrDefault("CLICKHOUSE_URL", "http://localhost:8123/?database=reports"),
		ClickHouseUser:     envOrDefault("CLICKHOUSE_USER", "bionicpro"),
		ClickHousePassword: envOrDefault("CLICKHOUSE_PASSWORD", "bionicpro"),
		KeycloakIssuer:     envOrDefault("KEYCLOAK_ISSUER", "http://localhost:8080/realms/reports-realm"),
		KeycloakJWKSURL:    envOrDefault("KEYCLOAK_JWKS_URL", "http://localhost:8080/realms/reports-realm/protocol/openid-connect/certs"),
		ExpectedAudience:   envOrDefault("EXPECTED_AUDIENCE", "reports-api"),
		S3Endpoint:         envOrDefault("S3_ENDPOINT", "localhost:9000"),
		S3AccessKey:        envOrDefault("S3_ACCESS_KEY", "bionicpro"),
		S3SecretKey:        envOrDefault("S3_SECRET_KEY", "bionicpro-secret"),
		S3Bucket:           envOrDefault("S3_BUCKET", "reports"),
		S3Secure:           strings.EqualFold(envOrDefault("S3_SECURE", "false"), "true"),
		CDNBaseURL:         strings.TrimRight(envOrDefault("CDN_BASE_URL", "http://localhost:8083"), "/"),
		ObjectKeySecret:    envOrDefault("OBJECT_KEY_SECRET", "local-report-object-key"),
	}

	httpClient := &http.Client{Timeout: 10 * time.Second}
	objects, err := newObjectStore(cfg)
	if err != nil {
		log.Fatal(err)
	}
	serverApp := &app{
		cfg:        cfg,
		httpClient: httpClient,
		keys:       &keyCache{url: cfg.KeycloakJWKSURL, client: httpClient, keys: map[string]*rsa.PublicKey{}},
		objects:    objects,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", serverApp.handleHealth)
	mux.HandleFunc("GET /reports", serverApp.handleReports)

	server := &http.Server{
		Addr:              cfg.ListenAddress,
		Handler:           securityHeaders(mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	log.Printf("reports-api started on %s", cfg.ListenAddress)
	log.Fatal(server.ListenAndServe())
}

func (a *app) handleHealth(w http.ResponseWriter, r *http.Request) {
	if _, err := a.queryClickHouse(r.Context(), "SELECT 1 FORMAT TabSeparated"); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unhealthy"})
		return
	}
	if err := a.objects.ready(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unhealthy"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *app) handleReports(w http.ResponseWriter, r *http.Request) {
	claims, err := a.authenticate(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
		return
	}
	if r.URL.Query().Has("user_id") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is taken from access token"})
		return
	}

	from, to, err := reportPeriod(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	watermark, err := a.watermark(r.Context())
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "report mart is not prepared yet"})
		return
	}
	if !to.Before(watermark.ProcessedUntil) {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":               "requested period has not been processed by Airflow yet",
			"last_available_date": watermark.ProcessedUntil.AddDate(0, 0, -1).Format(time.DateOnly),
		})
		return
	}

	objectKey := a.objects.reportKey(claims.PreferredUsername, from, to, watermark.UpdatedAt)
	exists, err := a.objects.exists(r.Context(), objectKey)
	if err != nil {
		log.Printf("check report object: %v", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "could not check report cache"})
		return
	}
	if exists {
		a.writeReportLink(w, objectKey, from, to, watermark, true)
		return
	}

	a.generationMu.Lock()
	defer a.generationMu.Unlock()

	exists, err = a.objects.exists(r.Context(), objectKey)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "could not check report cache"})
		return
	}
	if exists {
		a.writeReportLink(w, objectKey, from, to, watermark, true)
		return
	}

	report, err := a.generateReport(r.Context(), claims.PreferredUsername, from, to, watermark)
	if err != nil {
		log.Printf("generate report: %v", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "could not read report mart"})
		return
	}
	if err := a.objects.save(r.Context(), objectKey, report, from, to); err != nil {
		log.Printf("save report object: %v", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "could not save report cache"})
		return
	}
	a.writeReportLink(w, objectKey, from, to, watermark, false)
}

func (a *app) generateReport(ctx context.Context, username string, from, to time.Time, watermark etlWatermark) ([]byte, error) {
	query := fmt.Sprintf(`
SELECT
    user_id,
    client_id,
    full_name,
    prosthesis_model,
    crm_status,
    toString(report_date) AS report_date,
    events_count,
    avg_battery_level,
    avg_signal_quality,
    movements_count,
    formatDateTime(processed_at, '%%FT%%TZ', 'UTC') AS processed_at
FROM reports.user_report_mart_cdc AS mart
WHERE user_id = '%s'
  AND mart.report_date BETWEEN toDate('%s') AND toDate('%s')
ORDER BY mart.report_date
FORMAT JSONEachRow`, sqlString(username), from.Format(time.DateOnly), to.Format(time.DateOnly))

	rawRows, err := a.queryClickHouse(ctx, query)
	if err != nil {
		return nil, err
	}
	items := make([]json.RawMessage, 0)
	for _, line := range bytes.Split(bytes.TrimSpace(rawRows), []byte("\n")) {
		if len(bytes.TrimSpace(line)) > 0 {
			items = append(items, json.RawMessage(append([]byte(nil), line...)))
		}
	}

	return json.MarshalIndent(map[string]any{
		"user_id":         username,
		"from":            from.Format(time.DateOnly),
		"to":              to.Format(time.DateOnly),
		"processed_until": watermark.ProcessedUntil.Format(time.DateOnly),
		"etl_updated_at":  watermark.UpdatedAt.UTC().Format(time.RFC3339),
		"items":           items,
	}, "", "  ")
}

func (a *app) writeReportLink(w http.ResponseWriter, objectKey string, from, to time.Time, watermark etlWatermark, cached bool) {
	writeJSON(w, http.StatusOK, map[string]any{
		"download_url":    a.objects.downloadURL(objectKey),
		"cached":          cached,
		"from":            from.Format(time.DateOnly),
		"to":              to.Format(time.DateOnly),
		"processed_until": watermark.ProcessedUntil.Format(time.DateOnly),
	})
}

func (a *app) authenticate(r *http.Request) (tokenClaims, error) {
	authorization := r.Header.Get("Authorization")
	if !strings.HasPrefix(authorization, "Bearer ") {
		return tokenClaims{}, fmt.Errorf("missing bearer token")
	}
	encodedToken := strings.TrimPrefix(authorization, "Bearer ")
	parts := strings.Split(encodedToken, ".")
	if len(parts) != 3 {
		return tokenClaims{}, fmt.Errorf("invalid token")
	}

	var header tokenHeader
	if err := decodePart(parts[0], &header); err != nil || header.Algorithm != "RS256" || header.KeyID == "" {
		return tokenClaims{}, fmt.Errorf("invalid token header")
	}
	var claims tokenClaims
	if err := decodePart(parts[1], &claims); err != nil {
		return tokenClaims{}, fmt.Errorf("invalid token claims")
	}
	if claims.Issuer != a.cfg.KeycloakIssuer || claims.ExpiresAt <= time.Now().Unix() || claims.Subject == "" || claims.PreferredUsername == "" {
		return tokenClaims{}, fmt.Errorf("invalid token claims")
	}
	if !hasAudience(claims.Audience, a.cfg.ExpectedAudience) {
		return tokenClaims{}, fmt.Errorf("invalid token audience")
	}

	publicKey, err := a.keys.key(r.Context(), header.KeyID)
	if err != nil {
		return tokenClaims{}, err
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return tokenClaims{}, err
	}
	hash := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(publicKey, crypto.SHA256, hash[:], signature); err != nil {
		return tokenClaims{}, err
	}
	return claims, nil
}

func (k *keyCache) key(ctx context.Context, keyID string) (*rsa.PublicKey, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if key := k.keys[keyID]; key != nil && time.Since(k.lastUpdate) < 5*time.Minute {
		return key, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, k.url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := k.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("JWKS endpoint returned %d", resp.StatusCode)
	}
	var document jwksResponse
	if err := json.NewDecoder(resp.Body).Decode(&document); err != nil {
		return nil, err
	}
	keys := make(map[string]*rsa.PublicKey)
	for _, item := range document.Keys {
		if item.KeyType != "RSA" || item.Modulus == "" || item.Exponent == "" {
			continue
		}
		modulus, err := base64.RawURLEncoding.DecodeString(item.Modulus)
		if err != nil {
			continue
		}
		exponentBytes, err := base64.RawURLEncoding.DecodeString(item.Exponent)
		if err != nil {
			continue
		}
		exponent := 0
		for _, value := range exponentBytes {
			exponent = exponent<<8 + int(value)
		}
		keys[item.KeyID] = &rsa.PublicKey{N: new(big.Int).SetBytes(modulus), E: exponent}
	}
	k.keys = keys
	k.lastUpdate = time.Now()
	if key := keys[keyID]; key != nil {
		return key, nil
	}
	return nil, fmt.Errorf("token signing key not found")
}

func (a *app) watermark(ctx context.Context) (etlWatermark, error) {
	raw, err := a.queryClickHouse(ctx, `
SELECT
    formatDateTime(max(processed_until), '%F', 'UTC') AS processed_until,
    formatDateTime(max(updated_at), '%FT%TZ', 'UTC') AS updated_at
FROM reports.etl_watermark
WHERE pipeline = 'reports'
FORMAT JSONEachRow`)
	if err != nil || len(bytes.TrimSpace(raw)) == 0 {
		return etlWatermark{}, fmt.Errorf("watermark is unavailable")
	}
	var row struct {
		ProcessedUntil string `json:"processed_until"`
		UpdatedAt      string `json:"updated_at"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(raw), &row); err != nil {
		return etlWatermark{}, err
	}
	processedUntil, err := time.Parse(time.DateOnly, row.ProcessedUntil)
	if err != nil {
		return etlWatermark{}, err
	}
	updatedAt, err := time.Parse(time.RFC3339, row.UpdatedAt)
	if err != nil {
		return etlWatermark{}, err
	}
	return etlWatermark{ProcessedUntil: processedUntil, UpdatedAt: updatedAt}, nil
}

func (a *app) queryClickHouse(ctx context.Context, query string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.ClickHouseURL, strings.NewReader(query))
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(a.cfg.ClickHouseUser, a.cfg.ClickHousePassword)
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ClickHouse returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

func reportPeriod(r *http.Request) (time.Time, time.Time, error) {
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format(time.DateOnly)
	fromRaw := r.URL.Query().Get("from")
	toRaw := r.URL.Query().Get("to")
	if fromRaw == "" {
		fromRaw = yesterday
	}
	if toRaw == "" {
		toRaw = fromRaw
	}
	from, err := time.Parse(time.DateOnly, fromRaw)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("from must use YYYY-MM-DD format")
	}
	to, err := time.Parse(time.DateOnly, toRaw)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("to must use YYYY-MM-DD format")
	}
	if to.Before(from) {
		return time.Time{}, time.Time{}, fmt.Errorf("to must not be earlier than from")
	}
	if to.Sub(from) > 366*24*time.Hour {
		return time.Time{}, time.Time{}, fmt.Errorf("report period must not exceed 366 days")
	}
	return from, to, nil
}

func decodePart(encoded string, target any) error {
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return err
	}
	return json.Unmarshal(decoded, target)
}

func hasAudience(raw json.RawMessage, expected string) bool {
	var single string
	if json.Unmarshal(raw, &single) == nil {
		return single == expected
	}
	var multiple []string
	if json.Unmarshal(raw, &multiple) != nil {
		return false
	}
	for _, value := range multiple {
		if value == expected {
			return true
		}
	}
	return false
}

func sqlString(value string) string {
	return strings.ReplaceAll(value, "'", "''")
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}
