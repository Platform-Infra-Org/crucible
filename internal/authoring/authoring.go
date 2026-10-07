// Package authoring serves the content editor (spec docs-and-editor §3, §5, §6, §8): schema, validation, drafts and
// block inserts. Everything is gated by edits.Service.Authorize: only people allowed to propose edits to a training,
// never anyone enrolled in it.
package authoring

import (
	"context"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"crucible/internal/apperr"
	"crucible/internal/auth"
	"crucible/internal/content"
	"crucible/internal/content/blocks"
	"crucible/internal/edits"
	"crucible/internal/gitsync"
)

type Service struct {
	DB    *pgxpool.Pool
	Edits *edits.Service
	Log   *slog.Logger

	busy sync.Map // lowercased email → struct{}: one validate or insert at a time per user
}

// checkTimeout bounds one validate or insert. A var so tests can shorten it.
var checkTimeout = 20 * time.Second

type Problem struct {
	File string `json:"file"`
	Line int    `json:"line"`
	Msg  string `json:"msg"`
}

type ValidateReq struct {
	Training string       `json:"training"`
	BaseSHA  string       `json:"base_sha"`
	Ops      []gitsync.Op `json:"ops"`
}

// base is the training at sha for u. A base from the client is never trusted on its own (the bot's mirror also holds
// unmerged edit branches): it must be the head. Task 8 also allows the base of one of u's own drafts.
func (s *Service) base(_ context.Context, u *auth.User, training, sha string) (*content.Training, error) {
	t, head, err := s.Edits.Authorize(u, training)
	if err != nil {
		return nil, err
	}
	if sha != head {
		return nil, apperr.Wrap(apperr.Conflict, "the content changed since this draft started; rebase it")
	}
	return t, nil
}

// bounded runs fn with checkTimeout, one at a time per user. The user's slot is released only when fn really ends,
// so retrying a slow lint can't stack copies of it.
func (s *Service) bounded(ctx context.Context, email string, fn func()) error {
	key := strings.ToLower(email)
	if _, busy := s.busy.LoadOrStore(key, struct{}{}); busy {
		return apperr.Wrap(apperr.Conflict, "a check is already running; try again in a moment")
	}
	done := make(chan struct{})
	go func() {
		defer close(done) // runs last: the slot is free before the caller is released
		defer s.busy.Delete(key)
		fn()
	}()
	select {
	case <-done:
		return nil
	case <-time.After(checkTimeout):
		return apperr.Wrap(apperr.Unavailable, "the check took too long; try again")
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Schemas returns the JSON Schema of every file kind, for someone allowed to edit training.
func (s *Service) Schemas(u *auth.User, training string) (map[string]any, error) {
	if _, _, err := s.Edits.Authorize(u, training); err != nil {
		return nil, err
	}
	out := map[string]any{}
	for kind := range blocks.Kinds {
		out[kind] = blocks.Schema(kind)
	}
	return out, nil
}

// Validate loads the draft's base with in.Ops applied and returns its problems, each with a file and a line.
func (s *Service) Validate(ctx context.Context, u *auth.User, in ValidateReq) ([]Problem, error) {
	t, err := s.base(ctx, u, in.Training, in.BaseSHA)
	if err != nil {
		return nil, err
	}
	if len(in.Ops) > 0 {
		if err := gitsync.CheckOps(in.Ops); err != nil {
			return nil, err
		}
	}
	out := []Problem{}
	var ferr error
	if err := s.bounded(ctx, u.Email, func() {
		dir, _, cleanup, err := edits.Workspace(t, in.Ops)
		if err != nil {
			ferr = err
			return
		}
		defer cleanup()
		_, probs := content.Load(dir)
		out = locate(dir, probs)
	}); err != nil {
		return nil, err
	}
	return out, ferr
}

var (
	lineRE = regexp.MustCompile(`line (\d+)`)
	idRE   = regexp.MustCompile(`(?:question \d+ \(|task )([A-Za-z0-9_.-]+)`)
)

// locate gives each problem a file the author can open and a line in it.
// ponytail: heuristics over the loader's messages, which carry no positions: YAML's "line N"; "id: x" for a named
// question or task; a missing file is pinned on the YAML line that names it. Line 1 otherwise. Give content.Problem
// real positions if authors find these wrong.
func locate(dir string, probs []content.Problem) []Problem {
	out := []Problem{}
	for _, p := range probs {
		q := Problem{File: filepath.ToSlash(p.File), Line: 1, Msg: p.Msg}
		if m := lineRE.FindStringSubmatch(p.Msg); m != nil {
			q.Line, _ = strconv.Atoi(m[1])
		} else if m := idRE.FindStringSubmatch(p.Msg); m != nil {
			if n := lineOf(filepath.Join(dir, filepath.FromSlash(q.File)), "id: "+m[1]); n > 0 {
				q.Line = n
			}
		}
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(q.File))); err != nil {
			if f, n := mention(dir, q.File); f != "" {
				q.Msg = q.File + ": " + p.Msg
				q.File, q.Line = f, n
			}
		}
		out = append(out, q)
	}
	return out
}

// lineOf is the 1-based line of the first line containing s, or 0.
func lineOf(file, s string) int {
	b, err := os.ReadFile(file)
	if err != nil {
		return 0
	}
	for i, l := range strings.Split(string(b), "\n") {
		if strings.Contains(l, s) {
			return i + 1
		}
	}
	return 0
}

// mention finds the YAML file under modules/ (or training.yaml) that names missing, by its path relative to that
// file's folder, and the line.
func mention(dir, missing string) (string, int) {
	found, line := "", 0
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || found != "" || d.IsDir() || !strings.HasSuffix(p, ".yaml") {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		target := strings.TrimPrefix(missing, path.Dir(rel)+"/")
		if target == missing && path.Dir(rel) != "." {
			return nil // the missing file is not under this YAML's folder
		}
		if n := lineOf(p, target); n > 0 {
			found, line = rel, n
		}
		return nil
	})
	return found, line
}
