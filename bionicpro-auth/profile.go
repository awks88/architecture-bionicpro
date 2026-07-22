package main

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type profileStore struct {
	pool *pgxpool.Pool
}

func newProfileStore(ctx context.Context, databaseURL string) (*profileStore, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("configure profile database: %w", err)
	}
	store := &profileStore{pool: pool}
	if err := store.waitUntilReady(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	if err := store.migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return store, nil
}

func (s *profileStore) waitUntilReady(ctx context.Context) error {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := s.ping(ctx); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("connect to profile database: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func (s *profileStore) migrate(ctx context.Context) error {
	const statement = `
CREATE TABLE IF NOT EXISTS user_profiles (
    keycloak_user_id TEXT PRIMARY KEY,
    identity_provider TEXT NOT NULL,
    username TEXT NOT NULL,
    email TEXT NOT NULL,
    first_name TEXT NOT NULL,
    last_name TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
)`
	if _, err := s.pool.Exec(ctx, statement); err != nil {
		return fmt.Errorf("create user_profiles table: %w", err)
	}
	return nil
}

func (s *profileStore) upsert(ctx context.Context, info userInfo, now time.Time) error {
	provider := info.IdentityProvider
	if provider == "" {
		provider = "keycloak"
	}
	const statement = `
INSERT INTO user_profiles (
    keycloak_user_id, identity_provider, username, email, first_name, last_name, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (keycloak_user_id) DO UPDATE SET
    identity_provider = EXCLUDED.identity_provider,
    username = EXCLUDED.username,
    email = EXCLUDED.email,
    first_name = EXCLUDED.first_name,
    last_name = EXCLUDED.last_name,
    updated_at = EXCLUDED.updated_at`
	if _, err := s.pool.Exec(
		ctx,
		statement,
		info.Subject,
		provider,
		info.PreferredUsername,
		info.Email,
		info.GivenName,
		info.FamilyName,
		now,
	); err != nil {
		return fmt.Errorf("save user profile: %w", err)
	}
	return nil
}

func (s *profileStore) ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

func (s *profileStore) close() {
	s.pool.Close()
}
