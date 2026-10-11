package org

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"crucible/internal/apperr"
	"crucible/internal/gitsync"
	"crucible/internal/secrets"
)

// A repository is connected once and holds any number of trainings (one repo, a folder per training), so its
// credential is entered once, rotated once, and shows one status when it stops working. The credential is sealed by
// internal/secrets before it reaches Postgres and is never returned by any route: a caller learns that one is set,
// never what it is.

// RepoBody is a connect or a reconnect. Secret empty on a reconnect keeps the stored one, so a screen that never
// sees a credential cannot blank it by saving.
type RepoBody struct {
	URL      string `json:"url"`
	Auth     string `json:"auth"`     // none or token
	Username string `json:"username"` // optional beside a token
	Secret   string `json:"secret"`   // write-only
	Version  int64  `json:"version"`
}

// RepoView is a connected repository as a page sees it: no credential, and the URL with any userinfo masked.
type RepoView struct {
	ID        int64      `json:"id"`
	URL       string     `json:"url"`
	Auth      string     `json:"auth"`
	Username  string     `json:"username"`
	HasSecret bool       `json:"has_secret"`
	PublicKey string     `json:"public_key"` // the deploy key to add at the host; not a secret
	Status    string     `json:"status"`
	CanWrite  *bool      `json:"can_write"`
	LastError string     `json:"last_error"`
	CheckedAt *time.Time `json:"checked_at"`
	Version   int64      `json:"version"`
	Trainings []string   `json:"trainings"` // what breaks if this connection does
}

func cleanRepoBody(b RepoBody) (RepoBody, error) {
	b.URL, b.Username, b.Secret = strings.TrimSpace(b.URL), strings.TrimSpace(b.Username), strings.TrimSpace(b.Secret)
	if b.Auth == "" {
		b.Auth = "none"
	}
	if b.Auth != "none" && b.Auth != "token" && b.Auth != "ssh" {
		return b, apperr.Wrap(apperr.Invalid, "auth must be none, token or ssh")
	}
	if err := checkRepo(b.URL); err != nil {
		return b, err
	}
	if strings.Contains(b.URL, "@") && b.Auth == "token" {
		return b, apperr.Wrap(apperr.Invalid, "this URL already carries a credential; remove it from the URL and give the token on its own")
	}
	if b.Auth == "token" && !strings.HasPrefix(strings.ToLower(b.URL), "http") {
		return b, apperr.Wrap(apperr.Invalid, "a token is only used for http:// and https:// repositories")
	}
	if b.Auth == "ssh" && !IsSSH(b.URL) {
		return b, apperr.Wrap(apperr.Invalid, "a deploy key is only used for ssh:// and git@host:path repositories")
	}
	return b, nil
}

// ConnectRepo adds a repository, or reconnects one by id when id is not zero.
func (s *Store) ConnectRepo(ctx context.Context, actor string, id int64, b RepoBody, box *secrets.Box) (int64, error) {
	b, err := cleanRepoBody(b)
	if err != nil {
		return 0, err
	}
	if b.Auth == "token" && b.Secret == "" && id == 0 {
		return 0, apperr.Wrap(apperr.Invalid, "a token is needed to connect with one")
	}
	// Crucible makes the SSH keypair itself, so no private key ever travels to it: only the public half is shown,
	// to be added to the repository as a deploy key. Reconnecting keeps the pair unless a new one is asked for.
	public := ""
	if b.Auth == "ssh" && (id == 0 || b.Secret == "new") {
		if s.Secrets == nil {
			return 0, apperr.Wrap(apperr.Unavailable, "this instance cannot keep credentials: CRUCIBLE_SECRET_KEY is not set")
		}
		key, pub, err := newKeyPair("crucible-" + b.URL)
		if err != nil {
			return 0, err
		}
		b.Secret, public = key, pub
	} else if b.Secret == "new" {
		b.Secret = ""
	}
	sealed := ""
	if b.Secret != "" {
		if box == nil {
			return 0, apperr.Wrap(apperr.Unavailable, "this instance cannot keep credentials: CRUCIBLE_SECRET_KEY is not set")
		}
		if sealed, err = box.Seal(b.Secret); err != nil {
			return 0, err
		}
	}
	action, target := "repo.connect", b.URL
	if id != 0 {
		action = "repo.reconnect"
	}
	// The audit row records the URL and how it authenticates, never the credential: only that one was set.
	detail := map[string]any{"url": redactRepo(b.URL), "auth": b.Auth, "secret_set": b.Secret != ""}
	err = s.inTx(ctx, actor, action, target, detail, func(tx pgx.Tx) error {
		if id == 0 {
			err := tx.QueryRow(ctx, `INSERT INTO repositories (url, auth, username, secret, public_key)
				VALUES ($1, $2, $3, $4, $5) RETURNING id`, b.URL, b.Auth, b.Username, sealed, public).Scan(&id)
			var pg *pgconn.PgError
			if errors.As(err, &pg) && pg.Code == "23505" {
				return apperr.Wrap(apperr.Conflict, fmt.Sprintf("%s is already connected; reconnect that one instead", redactRepo(b.URL)))
			}
			return err
		}
		var v int64
		var prev string
		if err := tx.QueryRow(ctx, `SELECT version, url FROM repositories WHERE id = $1 FOR UPDATE`, id).Scan(&v, &prev); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return apperr.Wrap(apperr.NotFound, "no such repository")
			}
			return err
		}
		if b.Version != 0 && b.Version != v {
			return apperr.Wrap(apperr.Conflict, staleRepo)
		}
		detail["previous_url"] = redactRepo(prev)
		// A reconnect without a new secret keeps the stored one; switching to none clears it.
		_, err := tx.Exec(ctx, `UPDATE repositories SET url = $2, auth = $3, username = $4,
			secret = CASE WHEN $3 = 'none' THEN '' WHEN $5 <> '' THEN $5 ELSE secret END,
			public_key = CASE WHEN $3 <> 'ssh' THEN '' WHEN $6 <> '' THEN $6 ELSE public_key END,
			status = 'unknown', can_write = NULL, last_error = '', checked_at = NULL, version = version + 1
			WHERE id = $1`, id, b.URL, b.Auth, b.Username, sealed, public)
		return err
	})
	return id, err
}

const staleRepo = "someone else changed this repository while you had it open; reload and try again"

// DeleteRepo disconnects a repository no training uses. The repository itself is untouched.
func (s *Store) DeleteRepo(ctx context.Context, actor string, id int64) error {
	detail := map[string]any{}
	return s.inTx(ctx, actor, "repo.delete", fmt.Sprint(id), detail, func(tx pgx.Tx) error {
		var using string
		err := tx.QueryRow(ctx, `SELECT id FROM trainings WHERE repository = $1 ORDER BY id LIMIT 1`, id).Scan(&using)
		if err == nil {
			return apperr.Wrap(apperr.Conflict, fmt.Sprintf("training %q reads from this repository; delete it first", using))
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var url string
		err = tx.QueryRow(ctx, `DELETE FROM repositories WHERE id = $1 RETURNING url`, id).Scan(&url)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperr.Wrap(apperr.NotFound, "no such repository")
		}
		detail["url"] = redactRepo(url)
		if s.SSHDir != "" {
			if err := s.SSHDir.forget(id); err != nil {
				return err
			}
		}
		return err
	})
}

// Repos lists the connected repositories with the trainings that depend on each.
func (s *Store) Repos(ctx context.Context) ([]RepoView, error) {
	rows, err := s.DB.Query(ctx, `SELECT r.id, r.url, r.auth, r.username, r.secret <> '', r.public_key, r.status, r.can_write,
		r.last_error, r.checked_at, r.version, coalesce(array_agg(t.id ORDER BY t.id) FILTER (WHERE t.id IS NOT NULL), '{}')
		FROM repositories r LEFT JOIN trainings t ON t.repository = r.id GROUP BY r.id ORDER BY r.url`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RepoView{}
	for rows.Next() {
		var v RepoView
		if err := rows.Scan(&v.ID, &v.URL, &v.Auth, &v.Username, &v.HasSecret, &v.PublicKey, &v.Status, &v.CanWrite, &v.LastError,
			&v.CheckedAt, &v.Version, &v.Trainings); err != nil {
			return nil, err
		}
		v.URL = redactRepo(v.URL)
		out = append(out, v)
	}
	return out, rows.Err()
}

// RecordCheck stores what a connection test found. err is already redacted by gitsync.
func (s *Store) RecordCheck(ctx context.Context, id int64, canWrite *bool, failure string) error {
	status := "ok"
	if failure != "" {
		status = "failed"
	}
	_, err := s.DB.Exec(ctx, `UPDATE repositories SET status = $2, can_write = $3, last_error = $4, checked_at = now() WHERE id = $1`,
		id, status, canWrite, failure)
	return err
}

// fetchURL is the URL git is given for a repository: the stored URL with the credential put back in, in memory only.
// The credential is never stored in the URL, never returned by a route and never logged — gitsync.redact strips
// userinfo from git's own output, so a failure cannot carry it either.
func (s *Store) fetchURL(id int64, raw, auth, username, sealed string) (string, error) {
	box := s.Secrets
	if auth == "none" || sealed == "" || box == nil {
		return raw, nil
	}
	token, err := box.Open(sealed)
	if err != nil {
		return "", err
	}
	if auth == "ssh" {
		if s.SSHDir == "" {
			return "", apperr.Wrap(apperr.Unavailable, "this instance has nowhere to keep SSH keys")
		}
		return s.SSHDir.write(id, token, raw)
	}
	u, err := parseURL(raw)
	if err != nil {
		return "", err
	}
	if username == "" {
		username = "crucible" // GitHub ignores the username with a token; GitLab wants any non-empty one
	}
	u.User = url.UserPassword(username, token)
	return u.String(), nil
}

// Check tries a connected repository and records what it found, so a credential that stops working shows as one red
// row rather than as several trainings that quietly stop syncing.
func (s *Store) Check(ctx context.Context, id int64) {
	var raw, auth, username, sealed string
	if err := s.DB.QueryRow(ctx, `SELECT url, auth, username, secret FROM repositories WHERE id = $1`, id).
		Scan(&raw, &auth, &username, &sealed); err != nil {
		slog.Warn("cannot read the repository to check it", "repo", id, "err", err)
		return
	}
	failure := ""
	url, err := s.fetchURL(id, raw, auth, username, sealed)
	if err != nil {
		failure = err.Error()
	} else if err := gitsync.CheckRemote(ctx, url); err != nil {
		failure = err.Error() // already redacted by gitsync
	}
	if err := s.RecordCheck(ctx, id, nil, failure); err != nil {
		slog.Warn("cannot record the repository check", "repo", id, "err", err)
	}
}
