package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

var errSessionNotFound = errors.New("session not found")

type loginTransaction struct {
	CodeVerifier string `json:"code_verifier"`
	ReturnTo     string `json:"return_to"`
}

type userSession struct {
	UserID                string    `json:"user_id"`
	Username              string    `json:"username,omitempty"`
	AccessToken           string    `json:"access_token"`
	EncryptedRefreshToken string    `json:"encrypted_refresh_token"`
	AccessExpiresAt       time.Time `json:"access_expires_at"`
	AbsoluteExpiresAt     time.Time `json:"absolute_expires_at"`
	CreatedAt             time.Time `json:"created_at"`
}

type redisStore struct {
	client *redis.Client
}

func newRedisStore(address string) *redisStore {
	return &redisStore{
		client: redis.NewClient(&redis.Options{Addr: address}),
	}
}

func (s *redisStore) ping(ctx context.Context) error {
	return s.client.Ping(ctx).Err()
}

func (s *redisStore) close() error {
	return s.client.Close()
}

func (s *redisStore) saveLogin(ctx context.Context, state string, transaction loginTransaction, ttl time.Duration) error {
	payload, err := json.Marshal(transaction)
	if err != nil {
		return fmt.Errorf("marshal login transaction: %w", err)
	}
	return s.client.Set(ctx, loginKey(state), payload, ttl).Err()
}

func (s *redisStore) consumeLogin(ctx context.Context, state string) (loginTransaction, error) {
	payload, err := s.client.GetDel(ctx, loginKey(state)).Bytes()
	if errors.Is(err, redis.Nil) {
		return loginTransaction{}, errSessionNotFound
	}
	if err != nil {
		return loginTransaction{}, fmt.Errorf("consume login transaction: %w", err)
	}
	var transaction loginTransaction
	if err := json.Unmarshal(payload, &transaction); err != nil {
		return loginTransaction{}, fmt.Errorf("unmarshal login transaction: %w", err)
	}
	return transaction, nil
}

func (s *redisStore) saveSession(ctx context.Context, sessionID string, session userSession, ttl time.Duration) error {
	payload, err := json.Marshal(session)
	if err != nil {
		return fmt.Errorf("marshal session: %w", err)
	}
	return s.client.Set(ctx, sessionKey(sessionID), payload, ttl).Err()
}

func (s *redisStore) getSession(ctx context.Context, sessionID string) (userSession, string, error) {
	payload, err := s.client.Get(ctx, sessionKey(sessionID)).Bytes()
	if errors.Is(err, redis.Nil) {
		return userSession{}, "", errSessionNotFound
	}
	if err != nil {
		return userSession{}, "", fmt.Errorf("read session: %w", err)
	}
	var session userSession
	if err := json.Unmarshal(payload, &session); err != nil {
		return userSession{}, "", fmt.Errorf("unmarshal session: %w", err)
	}
	return session, string(payload), nil
}

var rotateSessionScript = redis.NewScript(`
local current = redis.call('GET', KEYS[1])
if not current or current ~= ARGV[1] then
  return 0
end
redis.call('SET', KEYS[2], ARGV[2], 'PX', ARGV[3])
redis.call('DEL', KEYS[1])
return 1
`)

func (s *redisStore) rotateSession(
	ctx context.Context,
	oldSessionID string,
	expectedPayload string,
	newSessionID string,
	session userSession,
	ttl time.Duration,
) (bool, error) {
	payload, err := json.Marshal(session)
	if err != nil {
		return false, fmt.Errorf("marshal rotated session: %w", err)
	}
	result, err := rotateSessionScript.Run(
		ctx,
		s.client,
		[]string{sessionKey(oldSessionID), sessionKey(newSessionID)},
		expectedPayload,
		string(payload),
		ttl.Milliseconds(),
	).Int()
	if err != nil {
		return false, fmt.Errorf("rotate session: %w", err)
	}
	return result == 1, nil
}

func (s *redisStore) deleteSession(ctx context.Context, sessionID string) error {
	return s.client.Del(ctx, sessionKey(sessionID)).Err()
}

func loginKey(state string) string {
	return "bionicpro:login:" + state
}

func sessionKey(sessionID string) string {
	return "bionicpro:session:" + sessionID
}
