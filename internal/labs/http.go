package labs

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"

	"crucible/internal/apperr"
	"crucible/internal/auth"
	"crucible/internal/httpx"
)

func (s *Service) Routes(r chi.Router) {
	const mod = "/api/programs/{team}/{training}/modules/{module}/lab"
	user := func(r *http.Request) *auth.User { return auth.UserFrom(r.Context()) }
	p := func(r *http.Request, k string) string { return chi.URLParam(r, k) }

	r.Get(mod, func(w http.ResponseWriter, r *http.Request) {
		v, err := s.ModuleLab(r.Context(), user(r), p(r, "team"), p(r, "training"), p(r, "module"))
		reply(w, v, err)
	})
	r.Post(mod, func(w http.ResponseWriter, r *http.Request) {
		v, err := s.Start(r.Context(), user(r), p(r, "team"), p(r, "training"), p(r, "module"))
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
	r.Get("/api/labs/{id}/terminals/{name}/ws", s.terminal)
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
	lab, _, err := s.labContent(inst)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	service := ""
	for _, t := range lab.Terminals {
		if t.Name == chi.URLParam(r, "name") {
			service = t.Service
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
	pty, err := s.Runners[inst.Runtime].OpenPTY(r.Context(), inst, service, cols, rows)
	if err != nil {
		httpx.Error(w, s.runnerErr(err))
		return
	}
	defer pty.Close()
	ws, err := websocket.Accept(w, r, nil) // same-origin only (default)
	if err != nil {
		return
	}
	defer ws.CloseNow()
	ctx := r.Context()

	go func() {
		buf := make([]byte, 32<<10)
		for {
			n, err := pty.Read(buf)
			if n > 0 && ws.Write(ctx, websocket.MessageBinary, buf[:n]) != nil {
				return
			}
			if err != nil {
				_ = ws.Close(websocket.StatusNormalClosure, "terminal closed")
				return
			}
		}
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
