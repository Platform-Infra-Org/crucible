// Package auth handles OIDC login, sessions and agent pairing tokens.
package auth

import (
	"context"
	"crucible/internal/apperr"
	"crucible/internal/audit"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
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
	Avatar     string `json:"avatar"` // one of Avatars, or "" for the person's initials
}

// Avatars are the forge icons a person may pick for their user card (web/src/components/Avatar.tsx draws them).
var Avatars = []string{"hammer", "anvil", "flame", "sword", "shield", "tongs", "helm", "ingot"}

type Store struct{ DB *pgxpool.Pool }

const userCols = "u.id, u.sub, u.email, u.name, u.theme, u.calm_motion, u.avatar"

func scanUser(row pgx.Row) (*User, error) {
	u := &User{}
	err := row.Scan(&u.ID, &u.Sub, &u.Email, &u.Name, &u.Theme, &u.CalmMotion, &u.Avatar)
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

// UpsertUser records a login. The first login of someone brought in by an import (org.Store.Import, sub
// "import:<email>") takes that row over, so their history follows them to the new identity provider; the email was
// verified by the provider (idClaims.problem).
func (s Store) UpsertUser(ctx context.Context, sub, email, name string) (*User, error) {
	if _, err := s.DB.Exec(ctx, `UPDATE users SET sub = $1 WHERE sub = 'import:' || lower($2)
		AND NOT EXISTS (SELECT 1 FROM users WHERE sub = $1)`, sub, email); err != nil {
		return nil, err
	}
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

// SetAvatar records the icon a person picked; "" goes back to their initials.
func (s Store) SetAvatar(ctx context.Context, userID int64, avatar string) error {
	if avatar != "" && !slices.Contains(Avatars, avatar) {
		return apperr.Wrap(apperr.Invalid, "unknown icon")
	}
	_, err := s.DB.Exec(ctx, `UPDATE users SET avatar = $2 WHERE id = $1`, userID, avatar)
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

// RevokeAgentTokens revokes every pairing token of the user (spec §5.1 "revocable"). Idempotent; audited only when a
// token was actually revoked, with actor (the admin, or "" for the user themselves) and the user's email as target.
func (s Store) RevokeAgentTokens(ctx context.Context, userID int64, actor string) error {
	tag, err := s.DB.Exec(ctx, `UPDATE agent_tokens SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL`, userID)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	var email string
	if err := s.DB.QueryRow(ctx, `SELECT email FROM users WHERE id = $1`, userID).Scan(&email); err != nil {
		return err
	}
	if actor == "" {
		actor = email
	}
	return audit.Log(ctx, s.DB, actor, "agent.token.revoke", email, nil, "")
}

// UserIDByEmail returns 0 when nobody has signed in with that address.
func (s Store) UserIDByEmail(ctx context.Context, email string) (int64, error) {
	var id int64
	err := s.DB.QueryRow(ctx, `SELECT id FROM users WHERE email = lower($1)`, strings.TrimSpace(email)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

func (s Store) UserByAgentToken(ctx context.Context, token string) (*User, error) {
	return scanUser(s.DB.QueryRow(ctx, `
		WITH t AS (UPDATE agent_tokens SET last_used_at = now()
		           WHERE token_hash = $1 AND revoked_at IS NULL RETURNING user_id)
		SELECT `+userCols+` FROM t JOIN users u ON u.id = t.user_id`, hash(token)))
}
