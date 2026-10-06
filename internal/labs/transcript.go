package labs

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"crucible/internal/apperr"
	"crucible/internal/auth"
)

const transcriptMax = 512 << 10

// tail keeps the last transcriptMax bytes of one terminal session's output. Memory stays under 2 × transcriptMax.
// Not safe for concurrent use: the PTY goroutine writes, and the reader runs only after that goroutine has finished.
type tail struct {
	buf     []byte
	dropped bool
}

func (t *tail) Write(p []byte) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > 2*transcriptMax { // amortised: one copy per transcriptMax bytes of output
		t.buf = append([]byte(nil), t.buf[len(t.buf)-transcriptMax:]...)
		t.dropped = true
	}
}

func (t *tail) Bytes() ([]byte, bool) {
	if len(t.buf) > transcriptMax {
		return t.buf[len(t.buf)-transcriptMax:], true
	}
	return t.buf, t.dropped
}

// saveTranscript stores a finished session for scorers (spec §7, §13). Failures are logged: a lost transcript must not
// break the trainee's terminal. ponytail: saved when the session closes, so a still-open session is not visible yet.
func (s *Service) saveTranscript(ctx context.Context, labID, terminal string, started time.Time, t *tail) {
	b, truncated := t.Bytes()
	if len(b) == 0 || s.Blobs == nil {
		return
	}
	key := "uploads/transcripts/" + labID + "/" + newLabID()
	if err := s.Blobs.Put(ctx, key, bytes.NewReader(b), int64(len(b))); err != nil {
		s.Log.Error("saving terminal transcript failed", "lab", labID, "err", err)
		return
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO terminal_transcripts (lab_id, terminal, blob_key, bytes, truncated, started_at, ended_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, labID, terminal, key, len(b), truncated, started, s.Now()); err != nil {
		s.Log.Error("recording terminal transcript failed", "lab", labID, "err", err)
	}
}

// Transcript opens a recorded session for the trainee who owns the lab or a scorer of its program; others get NotFound.
func (s *Service) Transcript(ctx context.Context, u *auth.User, labID string, id int64) (io.ReadCloser, error) {
	var key, team, training, owner string
	err := s.DB.QueryRow(ctx, `SELECT t.blob_key, l.team, l.training, u.email FROM terminal_transcripts t
		JOIN lab_instances l ON l.id = t.lab_id JOIN users u ON u.id = l.user_id
		WHERE t.id = $1 AND t.lab_id = $2`, id, labID).Scan(&key, &team, &training, &owner)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !strings.EqualFold(u.Email, owner) &&
		(s.Scoring == nil || !s.Scoring.CanScore(u, team, training, owner))) {
		return nil, apperr.Wrap(apperr.NotFound, "transcript not found")
	}
	if err != nil {
		return nil, err
	}
	if s.Blobs == nil {
		return nil, apperr.Wrap(apperr.Unavailable, "transcripts are not available")
	}
	return s.Blobs.Get(ctx, key)
}
