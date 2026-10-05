// Package auth handles OIDC login, sessions and agent pairing tokens.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type User struct {
	ID         int64  `json:"id"`
	Sub        string `json:"-"`
	Email      string `json:"email"`
	Name       string `json:"name"`
	Theme      string `json:"theme"`
	CalmMotion bool   `json:"calm_motion"`
}

type Store struct{ DB *pgxpool.Pool }

const userCols = "u.id, u.sub, u.email, u.name, u.theme, u.calm_motion"

func scanUser(row pgx.Row) (*User, error) {
	u := &User{}
	err := row.Scan(&u.ID, &u.Sub, &u.Email, &u.Name, &u.Theme, &u.CalmMotion)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return u, nil
}

func newToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func hash(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

func (s Store) UpsertUser(ctx context.Context, sub, email, name string) (*User, error) {
	return scanUser(s.DB.QueryRow(ctx, `
		INSERT INTO users AS u (sub, email, name) VALUES ($1, lower($2), $3)
		ON CONFLICT (sub) DO UPDATE SET email = EXCLUDED.email, name = EXCLUDED.name
		RETURNING `+userCols, sub, email, name))
}

func (s Store) CreateSession(ctx context.Context, userID int64, ttl time.Duration) (string, error) {
	tok := newToken()
	_, err := s.DB.Exec(ctx, `INSERT INTO sessions (id, user_id, expires_at) VALUES ($1, $2, $3)`,
		hash(tok), userID, time.Now().Add(ttl))
	return tok, err
}

func (s Store) UserBySession(ctx context.Context, token string) (*User, error) {
	return scanUser(s.DB.QueryRow(ctx, `SELECT `+userCols+` FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.id = $1 AND s.expires_at > now()`, hash(token)))
}

func (s Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.DB.Exec(ctx, `DELETE FROM sessions WHERE id = $1`, hash(token))
	return err
}

func (s Store) SetPrefs(ctx context.Context, userID int64, theme string, calm bool) error {
	_, err := s.DB.Exec(ctx, `UPDATE users SET theme = $2, calm_motion = $3 WHERE id = $1`, userID, theme, calm)
	return err
}

// CreateAgentToken issues a new pairing token and revokes the user's previous ones (one laptop at a time).
func (s Store) CreateAgentToken(ctx context.Context, userID int64) (string, error) {
	tok := newToken()
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE agent_tokens SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL`, userID); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO agent_tokens (user_id, token_hash) VALUES ($1, $2)`, userID, hash(tok)); err != nil {
		return "", err
	}
	return tok, tx.Commit(ctx)
}

func (s Store) UserByAgentToken(ctx context.Context, token string) (*User, error) {
	return scanUser(s.DB.QueryRow(ctx, `
		WITH t AS (UPDATE agent_tokens SET last_used_at = now()
		           WHERE token_hash = $1 AND revoked_at IS NULL RETURNING user_id)
		SELECT `+userCols+` FROM t JOIN users u ON u.id = t.user_id`, hash(token)))
}
