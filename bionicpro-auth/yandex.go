package main

import (
	"fmt"
	"io"
	"net/http"
	"strings"
)

const maxYandexProfileSize = 1 << 20

func (s *server) handleYandexUserInfo(w http.ResponseWriter, r *http.Request) {
	authorization := strings.TrimSpace(r.Header.Get("Authorization"))
	scheme, token, ok := strings.Cut(authorization, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(token) == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "bearer token is required"})
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, s.cfg.YandexUserInfoURL, nil)
	if err != nil {
		writeServerError(w, fmt.Errorf("create Yandex userinfo request: %w", err))
		return
	}
	req.Header.Set("Authorization", "OAuth "+strings.TrimSpace(token))
	req.Header.Set("Accept", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		writeServerError(w, fmt.Errorf("request Yandex userinfo: %w", err))
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, maxYandexProfileSize))
}
