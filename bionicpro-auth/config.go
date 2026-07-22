package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type config struct {
	ListenAddress       string
	InternalAddress     string
	PublicURL           string
	FrontendURL         string
	KeycloakPublicURL   string
	KeycloakInternalURL string
	KeycloakRealm       string
	KeycloakClientID    string
	KeycloakSecret      string
	RedisAddress        string
	ProfileDatabaseURL  string
	YandexUserInfoURL   string
	APIBaseURL          string
	CookieSecure        bool
	LoginTTL            time.Duration
	SessionIdleTTL      time.Duration
	SessionMaxTTL       time.Duration
	RefreshSkew         time.Duration
	HTTPTimeout         time.Duration
	EncryptionKey       []byte
}

func loadConfig() (config, error) {
	cfg := config{
		ListenAddress:       envOrDefault("LISTEN_ADDRESS", ":8000"),
		InternalAddress:     envOrDefault("INTERNAL_LISTEN_ADDRESS", ":8001"),
		PublicURL:           strings.TrimRight(envOrDefault("PUBLIC_URL", "http://localhost:8000"), "/"),
		FrontendURL:         strings.TrimRight(envOrDefault("FRONTEND_URL", "http://localhost:3000"), "/"),
		KeycloakPublicURL:   strings.TrimRight(envOrDefault("KEYCLOAK_PUBLIC_URL", "http://localhost:8080"), "/"),
		KeycloakInternalURL: strings.TrimRight(envOrDefault("KEYCLOAK_INTERNAL_URL", "http://localhost:8080"), "/"),
		KeycloakRealm:       envOrDefault("KEYCLOAK_REALM", "reports-realm"),
		KeycloakClientID:    envOrDefault("KEYCLOAK_CLIENT_ID", "bionicpro-auth"),
		KeycloakSecret:      os.Getenv("KEYCLOAK_CLIENT_SECRET"),
		RedisAddress:        envOrDefault("REDIS_ADDRESS", "localhost:6379"),
		ProfileDatabaseURL:  os.Getenv("PROFILE_DATABASE_URL"),
		YandexUserInfoURL:   envOrDefault("YANDEX_USERINFO_URL", "https://login.yandex.ru/info?format=json"),
		APIBaseURL:          strings.TrimRight(os.Getenv("API_BASE_URL"), "/"),
	}

	var err error
	if cfg.CookieSecure, err = boolEnv("COOKIE_SECURE", true); err != nil {
		return config{}, err
	}
	if cfg.LoginTTL, err = durationEnv("LOGIN_TTL", 5*time.Minute); err != nil {
		return config{}, err
	}
	if cfg.SessionIdleTTL, err = durationEnv("SESSION_IDLE_TTL", 30*time.Minute); err != nil {
		return config{}, err
	}
	if cfg.SessionMaxTTL, err = durationEnv("SESSION_MAX_TTL", 8*time.Hour); err != nil {
		return config{}, err
	}
	if cfg.RefreshSkew, err = durationEnv("ACCESS_TOKEN_REFRESH_SKEW", 15*time.Second); err != nil {
		return config{}, err
	}
	if cfg.HTTPTimeout, err = durationEnv("HTTP_TIMEOUT", 10*time.Second); err != nil {
		return config{}, err
	}

	if cfg.KeycloakSecret == "" {
		return config{}, fmt.Errorf("KEYCLOAK_CLIENT_SECRET is required")
	}
	if cfg.ProfileDatabaseURL == "" {
		return config{}, fmt.Errorf("PROFILE_DATABASE_URL is required")
	}

	encodedKey := os.Getenv("TOKEN_ENCRYPTION_KEY_BASE64")
	if encodedKey == "" {
		return config{}, fmt.Errorf("TOKEN_ENCRYPTION_KEY_BASE64 is required")
	}
	cfg.EncryptionKey, err = base64.StdEncoding.DecodeString(encodedKey)
	if err != nil {
		return config{}, fmt.Errorf("decode TOKEN_ENCRYPTION_KEY_BASE64: %w", err)
	}
	if len(cfg.EncryptionKey) != 32 {
		return config{}, fmt.Errorf("TOKEN_ENCRYPTION_KEY_BASE64 must decode to exactly 32 bytes")
	}
	if cfg.SessionIdleTTL <= 2*time.Minute {
		return config{}, fmt.Errorf("SESSION_IDLE_TTL must be longer than the two-minute access token lifetime")
	}
	if cfg.SessionMaxTTL < cfg.SessionIdleTTL {
		return config{}, fmt.Errorf("SESSION_MAX_TTL must be greater than or equal to SESSION_IDLE_TTL")
	}

	return cfg, nil
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func boolEnv(name string, fallback bool) (bool, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("parse %s: %w", name, err)
	}
	return value, nil
}

func durationEnv(name string, fallback time.Duration) (time.Duration, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", name, err)
	}
	return value, nil
}
