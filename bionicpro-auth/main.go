package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"time"
)

const (
	sessionCookieName = "bionicpro_session"
	loginCookieName   = "bionicpro_login"
)

type server struct {
	cfg         config
	store       *redisStore
	profiles    *profileStore
	keycloak    *keycloakClient
	tokenCipher *tokenCipher
	httpClient  *http.Client
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("configuration error: %v", err)
	}

	cipher, err := newTokenCipher(cfg.EncryptionKey)
	if err != nil {
		log.Fatalf("initialize token encryption: %v", err)
	}

	store := newRedisStore(cfg.RedisAddress)
	defer store.close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := waitForRedis(ctx, store); err != nil {
		log.Fatalf("connect to Redis: %v", err)
	}
	profiles, err := newProfileStore(ctx, cfg.ProfileDatabaseURL)
	if err != nil {
		log.Fatalf("initialize profile database: %v", err)
	}
	defer profiles.close()

	app := &server{
		cfg:         cfg,
		store:       store,
		profiles:    profiles,
		keycloak:    newKeycloakClient(cfg),
		tokenCipher: cipher,
		httpClient:  &http.Client{Timeout: cfg.HTTPTimeout},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", app.handleHealth)
	mux.HandleFunc("GET /auth/login", app.handleLogin)
	mux.HandleFunc("GET /auth/callback", app.handleCallback)
	mux.HandleFunc("GET /auth/session", app.handleSession)
	mux.HandleFunc("POST /auth/logout", app.handleLogout)
	mux.HandleFunc("GET /reports", app.handleReports)

	httpServer := &http.Server{
		Addr:              cfg.ListenAddress,
		Handler:           app.securityHeaders(app.cors(mux)),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	internalMux := http.NewServeMux()
	internalMux.HandleFunc("GET /userinfo", app.handleYandexUserInfo)
	internalHTTPServer := &http.Server{
		Addr:              cfg.InternalAddress,
		Handler:           internalMux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       30 * time.Second,
	}

	go func() {
		if err := internalHTTPServer.ListenAndServe(); err != nil {
			log.Fatalf("internal HTTP server: %v", err)
		}
	}()

	log.Printf("bionicpro-auth started on %s", cfg.ListenAddress)
	log.Fatal(httpServer.ListenAndServe())
}

func waitForRedis(ctx context.Context, store *redisStore) error {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := store.ping(ctx); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	if err := s.store.ping(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unhealthy"})
		return
	}
	if err := s.profiles.ping(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unhealthy"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *server) handleLogin(w http.ResponseWriter, r *http.Request) {
	state, err := randomURLSafe(32)
	if err != nil {
		writeServerError(w, err)
		return
	}
	verifier, err := randomURLSafe(32)
	if err != nil {
		writeServerError(w, err)
		return
	}

	returnTo := s.allowedReturnTo(r.URL.Query().Get("return_to"))
	transaction := loginTransaction{CodeVerifier: verifier, ReturnTo: returnTo}
	if err := s.store.saveLogin(r.Context(), state, transaction, s.cfg.LoginTTL); err != nil {
		writeServerError(w, err)
		return
	}
	s.setLoginCookie(w, state)
	http.Redirect(w, r, s.keycloak.authorizationURL(state, pkceChallenge(verifier)), http.StatusFound)
}

func (s *server) handleCallback(w http.ResponseWriter, r *http.Request) {
	if oauthError := r.URL.Query().Get("error"); oauthError != "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error":             oauthError,
			"error_description": r.URL.Query().Get("error_description"),
		})
		return
	}

	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	loginCookie, err := r.Cookie(loginCookieName)
	if err != nil || state == "" || code == "" || loginCookie.Value != state {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid OAuth callback state"})
		return
	}

	transaction, err := s.store.consumeLogin(r.Context(), state)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "login transaction expired or was already used"})
		return
	}
	s.clearLoginCookie(w)

	tokens, err := s.keycloak.exchangeCode(r.Context(), code, transaction.CodeVerifier)
	if err != nil {
		writeServerError(w, err)
		return
	}
	if tokens.RefreshToken == "" {
		writeServerError(w, fmt.Errorf("Keycloak response does not contain a refresh token"))
		return
	}
	info, err := s.keycloak.userInfo(r.Context(), tokens.AccessToken)
	if err != nil {
		writeServerError(w, err)
		return
	}
	if err := s.profiles.upsert(r.Context(), info, time.Now().UTC()); err != nil {
		writeServerError(w, err)
		return
	}
	encryptedRefreshToken, err := s.tokenCipher.encrypt(tokens.RefreshToken)
	if err != nil {
		writeServerError(w, err)
		return
	}

	now := time.Now().UTC()
	absoluteExpiry := now.Add(s.cfg.SessionMaxTTL)
	if tokens.RefreshExpiresIn > 0 {
		refreshExpiry := now.Add(time.Duration(tokens.RefreshExpiresIn) * time.Second)
		if refreshExpiry.Before(absoluteExpiry) {
			absoluteExpiry = refreshExpiry
		}
	}
	session := userSession{
		UserID:                info.Subject,
		Username:              info.PreferredUsername,
		AccessToken:           tokens.AccessToken,
		EncryptedRefreshToken: encryptedRefreshToken,
		AccessExpiresAt:       now.Add(time.Duration(tokens.ExpiresIn) * time.Second),
		AbsoluteExpiresAt:     absoluteExpiry,
		CreatedAt:             now,
	}
	sessionID, err := randomURLSafe(32)
	if err != nil {
		writeServerError(w, err)
		return
	}
	ttl, ok := s.sessionTTL(session, now)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "refresh token is already expired"})
		return
	}
	if err := s.store.saveSession(r.Context(), sessionID, session, ttl); err != nil {
		writeServerError(w, err)
		return
	}
	s.setSessionCookie(w, sessionID, ttl)
	http.Redirect(w, r, transaction.ReturnTo, http.StatusFound)
}

func (s *server) handleSession(w http.ResponseWriter, r *http.Request) {
	session, ok := s.authenticateAndRotate(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": true,
		"user_id":       session.UserID,
		"username":      session.Username,
	})
}

func (s *server) handleReports(w http.ResponseWriter, r *http.Request) {
	session, ok := s.authenticateAndRotate(w, r)
	if !ok {
		return
	}
	if s.cfg.APIBaseURL == "" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="bionicpro-report.txt"`)
		_, _ = fmt.Fprintf(w, "BionicPRO usage report\nUser: %s\nGenerated: %s\n", session.Username, time.Now().Format(time.RFC3339))
		return
	}

	targetURL := s.cfg.APIBaseURL + "/reports"
	if r.URL.RawQuery != "" {
		targetURL += "?" + r.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, targetURL, nil)
	if err != nil {
		writeServerError(w, err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+session.AccessToken)
	req.Header.Set("Accept", r.Header.Get("Accept"))
	resp, err := s.httpClient.Do(req)
	if err != nil {
		writeServerError(w, fmt.Errorf("request reports API: %w", err))
		return
	}
	defer resp.Body.Close()
	for _, header := range []string{"Content-Type", "Content-Disposition", "Content-Length"} {
		if value := resp.Header.Get(header); value != "" {
			w.Header().Set(header, value)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (s *server) handleLogout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(sessionCookieName)
	if err == nil && cookie.Value != "" {
		if session, _, readErr := s.store.getSession(r.Context(), cookie.Value); readErr == nil {
			if refreshToken, decryptErr := s.tokenCipher.decrypt(session.EncryptedRefreshToken); decryptErr == nil {
				if logoutErr := s.keycloak.logout(r.Context(), refreshToken); logoutErr != nil {
					log.Printf("Keycloak logout failed: %v", logoutErr)
				}
			}
			_ = s.store.deleteSession(r.Context(), cookie.Value)
		}
	}
	s.clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) authenticateAndRotate(w http.ResponseWriter, r *http.Request) (userSession, bool) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
		return userSession{}, false
	}

	session, originalPayload, err := s.store.getSession(r.Context(), cookie.Value)
	if err != nil {
		s.clearSessionCookie(w)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "session expired"})
		return userSession{}, false
	}

	now := time.Now().UTC()
	if !now.Before(session.AbsoluteExpiresAt) {
		_ = s.store.deleteSession(r.Context(), cookie.Value)
		s.clearSessionCookie(w)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "session expired"})
		return userSession{}, false
	}

	if !now.Add(s.cfg.RefreshSkew).Before(session.AccessExpiresAt) {
		if err := s.refreshSession(r.Context(), &session, now); err != nil {
			_ = s.store.deleteSession(r.Context(), cookie.Value)
			s.clearSessionCookie(w)
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "session could not be refreshed"})
			return userSession{}, false
		}
	}

	newSessionID, err := randomURLSafe(32)
	if err != nil {
		writeServerError(w, err)
		return userSession{}, false
	}
	ttl, ok := s.sessionTTL(session, now)
	if !ok {
		s.clearSessionCookie(w)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "session expired"})
		return userSession{}, false
	}
	rotated, err := s.store.rotateSession(r.Context(), cookie.Value, originalPayload, newSessionID, session, ttl)
	if err != nil {
		writeServerError(w, err)
		return userSession{}, false
	}
	if !rotated {
		s.clearSessionCookie(w)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "session was already rotated"})
		return userSession{}, false
	}
	s.setSessionCookie(w, newSessionID, ttl)
	return session, true
}

func (s *server) refreshSession(ctx context.Context, session *userSession, now time.Time) error {
	refreshToken, err := s.tokenCipher.decrypt(session.EncryptedRefreshToken)
	if err != nil {
		return err
	}
	tokens, err := s.keycloak.refresh(ctx, refreshToken)
	if err != nil {
		return err
	}
	if tokens.RefreshToken == "" {
		tokens.RefreshToken = refreshToken
	}
	encryptedRefreshToken, err := s.tokenCipher.encrypt(tokens.RefreshToken)
	if err != nil {
		return err
	}
	session.AccessToken = tokens.AccessToken
	session.EncryptedRefreshToken = encryptedRefreshToken
	session.AccessExpiresAt = now.Add(time.Duration(tokens.ExpiresIn) * time.Second)
	if tokens.RefreshExpiresIn > 0 {
		refreshExpiry := now.Add(time.Duration(tokens.RefreshExpiresIn) * time.Second)
		if refreshExpiry.Before(session.AbsoluteExpiresAt) {
			session.AbsoluteExpiresAt = refreshExpiry
		}
	}
	return nil
}

func (s *server) sessionTTL(session userSession, now time.Time) (time.Duration, bool) {
	remaining := session.AbsoluteExpiresAt.Sub(now)
	if !session.AbsoluteExpiresAt.After(now) || remaining <= 0 {
		return 0, false
	}
	if remaining < s.cfg.SessionIdleTTL {
		return remaining, true
	}
	return s.cfg.SessionIdleTTL, true
}

func (s *server) allowedReturnTo(raw string) string {
	if raw == "" {
		return s.cfg.FrontendURL
	}
	frontend, err := url.Parse(s.cfg.FrontendURL)
	if err != nil {
		return s.cfg.FrontendURL
	}
	target, err := url.Parse(raw)
	if err != nil || target.Scheme != frontend.Scheme || target.Host != frontend.Host || target.User != nil {
		return s.cfg.FrontendURL
	}
	return target.String()
}

func (s *server) setLoginCookie(w http.ResponseWriter, state string) {
	http.SetCookie(w, &http.Cookie{
		Name:     loginCookieName,
		Value:    state,
		Path:     "/auth/callback",
		MaxAge:   int(s.cfg.LoginTTL.Seconds()),
		HttpOnly: true,
		Secure:   s.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *server) clearLoginCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     loginCookieName,
		Path:     "/auth/callback",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *server) setSessionCookie(w http.ResponseWriter, sessionID string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    sessionID,
		Path:     "/",
		MaxAge:   int(ttl.Seconds()),
		HttpOnly: true,
		Secure:   s.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && origin != s.cfg.FrontendURL {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "origin is not allowed"})
			return
		}
		if origin == s.cfg.FrontendURL {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Add("Vary", "Origin")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method == http.MethodPost && origin != s.cfg.FrontendURL {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "valid Origin header is required"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func writeServerError(w http.ResponseWriter, err error) {
	log.Printf("request failed: %v", err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
