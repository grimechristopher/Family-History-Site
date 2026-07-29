package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"time"
)

// hashToken means only a digest is stored, so a leaked database dump cannot be
// replayed as a live session.
func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func (s *Store) CreateSession(ctx context.Context, userID int64, ttl time.Duration, userAgent string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate session token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)

	_, err := s.Pool.Exec(ctx, `
		INSERT INTO family.sessions (token_hash, user_id, expires_at, user_agent)
		VALUES ($1, $2, now() + make_interval(secs => $3), $4)`,
		hashToken(token), userID, ttl.Seconds(), userAgent)
	if err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}
	return token, nil
}

// UserBySessionToken resolves a cookie value to a user and refreshes last-seen
// timestamps, so it is possible to tell whether a parent has opened the site.
func (s *Store) UserBySessionToken(ctx context.Context, token string) (*User, error) {
	if token == "" {
		return nil, ErrNotFound
	}
	hash := hashToken(token)

	u, err := scanUser(s.Pool.QueryRow(ctx, `
		SELECT `+prefixed(userColumns, "u.")+`
		FROM family.sessions s
		JOIN family.users u ON u.id = s.user_id
		WHERE s.token_hash = $1 AND s.expires_at > now()`, hash))
	if err != nil {
		return nil, err
	}

	if _, err := s.Pool.Exec(ctx,
		`UPDATE family.sessions SET last_used_at = now() WHERE token_hash = $1`, hash); err != nil {
		return nil, err
	}
	if _, err := s.Pool.Exec(ctx,
		`UPDATE family.users SET last_seen_at = now() WHERE id = $1`, u.ID); err != nil {
		return nil, err
	}
	return u, nil
}

func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.Pool.Exec(ctx,
		`DELETE FROM family.sessions WHERE token_hash = $1`, hashToken(token))
	return err
}

func (s *Store) DeleteExpiredSessions(ctx context.Context) (int64, error) {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM family.sessions WHERE expires_at < now()`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
