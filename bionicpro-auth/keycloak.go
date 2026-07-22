package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type keycloakClient struct {
	cfg        config
	httpClient *http.Client
}

type tokenResponse struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	IDToken          string `json:"id_token"`
	ExpiresIn        int64  `json:"expires_in"`
	RefreshExpiresIn int64  `json:"refresh_expires_in"`
	TokenType        string `json:"token_type"`
}

type userInfo struct {
	Subject           string `json:"sub"`
	PreferredUsername string `json:"preferred_username"`
	Email             string `json:"email"`
	GivenName         string `json:"given_name"`
	FamilyName        string `json:"family_name"`
	IdentityProvider  string `json:"identity_provider"`
}

func newKeycloakClient(cfg config) *keycloakClient {
	return &keycloakClient{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: cfg.HTTPTimeout,
		},
	}
}

func (k *keycloakClient) authorizationURL(state, challenge string) string {
	params := url.Values{
		"client_id":             {k.cfg.KeycloakClientID},
		"redirect_uri":          {k.cfg.PublicURL + "/auth/callback"},
		"response_type":         {"code"},
		"scope":                 {"openid profile email"},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	return k.protocolEndpoint(k.cfg.KeycloakPublicURL, "auth") + "?" + params.Encode()
}

func (k *keycloakClient) exchangeCode(ctx context.Context, code, verifier string) (tokenResponse, error) {
	return k.requestTokens(ctx, url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {k.cfg.KeycloakClientID},
		"client_secret": {k.cfg.KeycloakSecret},
		"redirect_uri":  {k.cfg.PublicURL + "/auth/callback"},
		"code":          {code},
		"code_verifier": {verifier},
	})
}

func (k *keycloakClient) refresh(ctx context.Context, refreshToken string) (tokenResponse, error) {
	return k.requestTokens(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {k.cfg.KeycloakClientID},
		"client_secret": {k.cfg.KeycloakSecret},
		"refresh_token": {refreshToken},
	})
}

func (k *keycloakClient) requestTokens(ctx context.Context, values url.Values) (tokenResponse, error) {
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		k.protocolEndpoint(k.cfg.KeycloakInternalURL, "token"),
		strings.NewReader(values.Encode()),
	)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := k.httpClient.Do(req)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("request tokens: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return tokenResponse{}, fmt.Errorf("Keycloak token endpoint returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var tokens tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokens); err != nil {
		return tokenResponse{}, fmt.Errorf("decode token response: %w", err)
	}
	if tokens.AccessToken == "" {
		return tokenResponse{}, fmt.Errorf("Keycloak response does not contain an access token")
	}
	return tokens, nil
}

func (k *keycloakClient) userInfo(ctx context.Context, accessToken string) (userInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, k.protocolEndpoint(k.cfg.KeycloakInternalURL, "userinfo"), nil)
	if err != nil {
		return userInfo{}, fmt.Errorf("create userinfo request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := k.httpClient.Do(req)
	if err != nil {
		return userInfo{}, fmt.Errorf("request userinfo: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return userInfo{}, fmt.Errorf("Keycloak userinfo endpoint returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var info userInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return userInfo{}, fmt.Errorf("decode userinfo response: %w", err)
	}
	if info.Subject == "" {
		return userInfo{}, fmt.Errorf("userinfo response does not contain sub")
	}
	return info, nil
}

func (k *keycloakClient) logout(ctx context.Context, refreshToken string) error {
	values := url.Values{
		"client_id":     {k.cfg.KeycloakClientID},
		"client_secret": {k.cfg.KeycloakSecret},
		"refresh_token": {refreshToken},
	}
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		k.protocolEndpoint(k.cfg.KeycloakInternalURL, "logout"),
		strings.NewReader(values.Encode()),
	)
	if err != nil {
		return fmt.Errorf("create logout request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := k.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request logout: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Keycloak logout endpoint returned %d", resp.StatusCode)
	}
	return nil
}

func (k *keycloakClient) protocolEndpoint(baseURL, endpoint string) string {
	return fmt.Sprintf(
		"%s/realms/%s/protocol/openid-connect/%s",
		strings.TrimRight(baseURL, "/"),
		url.PathEscape(k.cfg.KeycloakRealm),
		endpoint,
	)
}
