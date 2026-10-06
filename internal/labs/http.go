package labs

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"

	"crucible/internal/apperr"
	"crucible/internal/auth"
	"crucible/internal/httpx"
	"crucible/internal/scoring"
)

func (s *Service) Routes(r chi.Router) {
	const mod = "/api/programs/{team}/{training}/modules/{module}/lab"
	user := func(r *http.Request) *auth.User { return auth.UserFrom(r.Context()) }
	p := func(r *http.Request, k string) string { return chi.URLParam(r, k) }

	r.Get("/api/kill-switch", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.KillSwitch(r.Context())
		reply(w, v, err)
	})
	r.Post("/api/admin/kill-switch", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Enabled bool `json:"enabled"`
		}
		if err := httpx.Read(r, &body); err != nil {
			httpx.Error(w, err)
			return
		}
		v, err := s.SetKillSwitch(r.Context(), user(r), body.Enabled)
		reply(w, v, err)
	})

	r.Get("/api/ledger", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.Ledger(r.Context(), user(r))
		reply(w, v, err)
	})
	r.Post("/api/admin/finops/refresh", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.RefreshFinOps(r.Context(), user(r))
		reply(w, v, err)
	})

	r.Get(mod, func(w http.ResponseWriter, r *http.Request) {
		v, err := s.ModuleLab(r.Context(), user(r), p(r, "team"), p(r, "training"), p(r, "module"))
		reply(w, v, err)
	})
	r.Post(mod, func(w http.ResponseWriter, r *http.Request) {
		v, err := s.Start(r.Context(), user(r), p(r, "team"), p(r, "training"), p(r, "module"))
		reply(w, v, err)
	})
	r.Get("/api/approvals", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.Approvals(r.Context(), user(r))
		reply(w, v, err)
	})
	r.Post("/api/approvals/{id}", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Approve *bool  `json:"approve"` // required: `{}` must never silently reject
			Note    string `json:"note"`
		}
		if err := httpx.Read(r, &body); err != nil {
			httpx.Error(w, err)
			return
		}
		if body.Approve == nil {
			httpx.Error(w, apperr.Wrap(apperr.Invalid, "approve (true or false) is required"))
			return
		}
		st, err := s.Decide(r.Context(), user(r), p(r, "id"), *body.Approve, body.Note)
		reply(w, map[string]State{"state": st}, err)
	})
	r.Post("/api/approvals/{id}/extension", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Approve *bool  `json:"approve"` // required, as for requests
			Note    string `json:"note"`
		}
		if err := httpx.Read(r, &body); err != nil {
			httpx.Error(w, err)
			return
		}
		if body.Approve == nil {
			httpx.Error(w, apperr.Wrap(apperr.Invalid, "approve (true or false) is required"))
			return
		}
		if err := s.DecideExtension(r.Context(), user(r), p(r, "id"), *body.Approve, body.Note); err != nil {
			httpx.Error(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	r.Get("/api/labs", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.Mine(r.Context(), user(r))
		reply(w, v, err)
	})
	r.Get("/api/labs/{id}", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.Get(r.Context(), user(r), p(r, "id"))
		reply(w, v, err)
	})
	r.Delete("/api/labs/{id}", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.End(r.Context(), user(r), p(r, "id"))
		reply(w, v, err)
	})
	r.Post("/api/labs/{id}/activity", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.Activity(r.Context(), user(r), p(r, "id"))
		reply(w, v, err)
	})
	r.Post("/api/labs/{id}/extend", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.Extend(r.Context(), user(r), p(r, "id"))
		reply(w, v, err)
	})
	r.Get("/api/labs/{id}/tasks/{task}", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.OpenTask(r.Context(), user(r), p(r, "id"), p(r, "task"))
		reply(w, v, err)
	})
	r.Post("/api/labs/{id}/tasks/{task}/check", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Answer string `json:"answer"`
		}
		if err := httpx.Read(r, &body); err != nil {
			httpx.Error(w, err)
			return
		}
		v, err := s.Check(r.Context(), user(r), p(r, "id"), p(r, "task"), body.Answer)
		reply(w, v, err)
	})
	r.Post("/api/labs/{id}/tasks/{task}/hint", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.RevealHint(r.Context(), user(r), p(r, "id"), p(r, "task"))
		reply(w, v, err)
	})
	r.Post("/api/labs/{id}/tasks/{task}/reset", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.ResetTask(r.Context(), user(r), p(r, "id"), p(r, "task"))
		reply(w, v, err)
	})
	r.Post("/api/labs/{id}/tasks/{task}/skip", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.Skip(r.Context(), user(r), p(r, "id"), p(r, "task"))
		reply(w, v, err)
	})
	r.Post("/api/labs/{id}/tasks/{task}/submit", func(w http.ResponseWriter, r *http.Request) {
		if _, err := s.owned(r.Context(), user(r), p(r, "id")); err != nil { // before spooling any upload
			httpx.Error(w, err)
			return
		}
		notes, files, done, err := scoring.ReadForm(w, r)
		defer done()
		if err != nil {
			httpx.Error(w, err)
			return
		}
		v, err := s.SubmitReview(r.Context(), user(r), p(r, "id"), p(r, "task"), notes, files)
		reply(w, v, err)
	})
	r.Get("/api/labs/{id}/terminals/{name}/ws", s.terminal)
	r.Get("/api/labs/{id}/transcripts/{tid}", func(w http.ResponseWriter, r *http.Request) {
		tid, err := strconv.ParseInt(p(r, "tid"), 10, 64)
		if err != nil {
			httpx.Error(w, apperr.Wrap(apperr.NotFound, "transcript not found"))
			return
		}
		rc, err := s.Transcript(r.Context(), user(r), p(r, "id"), tid)
		if err != nil {
			httpx.Error(w, err)
			return
		}
		defer rc.Close()
		h := w.Header() // blob Get has no content type; terminal output is untrusted, never rendered
		h.Set("Content-Type", "text/plain; charset=utf-8")
		h.Set("Content-Disposition", `attachment; filename="transcript.txt"`)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Security-Policy", "sandbox")
		h.Set("Cache-Control", "private, no-store")
		_, _ = io.Copy(w, rc)
	})
}

func reply(w http.ResponseWriter, v any, err error) {
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}

func (s *Service) terminal(w http.ResponseWriter, r *http.Request) {
	inst, err := s.owned(r.Context(), auth.UserFrom(r.Context()), chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, err)
		return
	}
	if inst.State != Ready {
		httpx.Error(w, apperr.Wrap(apperr.Conflict, "the lab is not ready"))
		return
	}
	lab, _, err := s.labContent(r.Context(), inst)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	service := ""
	for _, t := range lab.Terminals {
		if t.Name == chi.URLParam(r, "name") {
			service = t.Service
			break
		}
	}
	if service == "" {
		httpx.Error(w, apperr.Wrap(apperr.NotFound, "terminal not found"))
		return
	}
	cols, _ := strconv.Atoi(r.URL.Query().Get("cols"))
	rows, _ := strconv.Atoi(r.URL.Query().Get("rows"))
	if cols <= 0 || rows <= 0 {
		cols, rows = 120, 32
	}
	ws, err := websocket.Accept(w, r, nil) // same-origin only (default); before any exec
	if err != nil {
		return
	}
	defer ws.CloseNow()
	rn, err := s.runner(inst.Runtime)
	var pty PTY
	if err == nil {
		pty, err = rn.OpenPTY(r.Context(), inst, service, cols, rows)
	}
	if err != nil {
		reason := s.runnerErr(err).Error()
		if len(reason) > 120 {
			reason = reason[:120] // close reason limit is 123 bytes
		}
		_ = ws.Close(websocket.StatusTryAgainLater, reason)
		return
	}
	ws.SetReadLimit(1 << 20)
	ctx := r.Context()
	rec, started := &tail{}, s.Now() // spec §7: the scorer sees what the terminal printed

	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 32<<10)
		for {
			n, err := pty.Read(buf)
			if n > 0 {
				rec.Write(buf[:n]) // only this goroutine touches rec until done is closed
				if ws.Write(ctx, websocket.MessageBinary, buf[:n]) != nil {
					return
				}
			}
			if err != nil {
				_ = ws.Close(websocket.StatusNormalClosure, "terminal closed")
				return
			}
		}
	}()
	defer func() {
		_ = pty.Close()
		ws.CloseNow()
		<-done
		saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second) // a hung store must not pin the handler
		s.saveTranscript(saveCtx, inst.ID, chi.URLParam(r, "name"), started, rec)
		cancel()
	}()
	for {
		typ, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		if typ == websocket.MessageText {
			var size struct {
				Cols int `json:"cols"`
				Rows int `json:"rows"`
			}
			if json.Unmarshal(data, &size) == nil && size.Cols > 0 && size.Rows > 0 {
				_ = pty.Resize(size.Cols, size.Rows)
			}
			continue
		}
		s.Touch(ctx, inst.ID)
		if _, err := pty.Write(data); err != nil {
			return
		}
	}
}
