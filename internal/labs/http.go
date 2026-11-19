package labs

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"

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
	r.Put("/api/admin/account-spend/marks", func(w http.ResponseWriter, r *http.Request) {
		var b MarkBody
		if err := httpx.Read(r, &b); err != nil {
			httpx.Error(w, err)
			return
		}
		v, err := s.SetBilledMark(r.Context(), user(r), b)
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
		var opt StartOptions
		if r.ContentLength != 0 {
			if err := httpx.Read(r, &opt); err != nil {
				httpx.Error(w, err)
				return
			}
		}
		v, err := s.Start(r.Context(), user(r), p(r, "team"), p(r, "training"), p(r, "module"), opt)
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
	r.Post("/api/labs/{id}/console", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.Console(r.Context(), user(r), p(r, "id"))
		reply(w, v, err)
	})
	r.Post("/api/labs/{id}/extend", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.Extend(r.Context(), user(r), p(r, "id"))
		reply(w, v, err)
	})
	r.Post("/api/labs/{id}/wake", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.Wake(r.Context(), user(r), p(r, "id"))
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
	r.Delete("/api/labs/{id}/terminals/{name}", func(w http.ResponseWriter, r *http.Request) {
		err := s.CloseTerminal(r.Context(), user(r), p(r, "id"), p(r, "name"), r.URL.Query().Get("tab"))
		reply(w, struct{}{}, err)
	})
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
	service, fixed := "", false
	var cols, rows int
	for _, t := range lab.Terminals {
		if t.Name == chi.URLParam(r, "name") {
			service = t.Service
			cols, rows, fixed = t.Fixed() // a fixed-size terminal (a serial console) ignores the browser's size
			break
		}
	}
	if service == "" {
		httpx.Error(w, apperr.Wrap(apperr.NotFound, "terminal not found"))
		return
	}
	if !fixed {
		cols, _ = strconv.Atoi(r.URL.Query().Get("cols"))
		rows, _ = strconv.Atoi(r.URL.Query().Get("rows"))
		if cols <= 0 || rows <= 0 {
			cols, rows = 120, 32
		}
	}
	// A tab is one session: "console" and a second copy of it, "console #2", are separate shells of the same terminal.
	tab := r.URL.Query().Get("tab")
	if tab == "" {
		tab = chi.URLParam(r, "name")
	}
	if tabTerminal(tab) != chi.URLParam(r, "name") {
		httpx.Error(w, apperr.Wrap(apperr.NotFound, "terminal not found"))
		return
	}
	ws, err := websocket.Accept(w, r, nil) // same-origin only (default); before any exec
	if err != nil {
		return
	}
	defer ws.CloseNow()
	// The session outlives this websocket: a refresh or a closed tab comes back to the same shell (spec §8.3), and
	// what it printed is replayed onto the new one. Only the newest websocket watches it, so nobody types blind.
	ts, err := s.session(r.Context(), inst, tab, chi.URLParam(r, "name"), service, cols, rows)
	if err != nil {
		reason := s.runnerErr(err).Error()
		if len(reason) > 120 {
			reason = reason[:120] // close reason limit is 123 bytes
		}
		_ = ws.Close(websocket.StatusTryAgainLater, reason)
		return
	}
	ws.SetReadLimit(1 << 20)
	ctx, drop := context.WithCancel(r.Context())
	defer drop()
	v := &termViewer{
		write: func(b []byte) error { return ws.Write(ctx, websocket.MessageBinary, b) },
		drop:  drop,
	}
	ts.attach(v)
	defer ts.detach(v)
	pty := ts.pty
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
			if !fixed && json.Unmarshal(data, &size) == nil && size.Cols > 0 && size.Rows > 0 {
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
