// Package httpx holds the JSON and error helpers every handler uses.
package httpx

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"crucible/internal/apperr"
)

func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Read decodes a JSON body (max 1 MiB) and rejects unknown fields.
func Read(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return apperr.Wrap(apperr.Invalid, err.Error())
	}
	return nil
}

func Error(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, apperr.NotFound):
		status = http.StatusNotFound
	case errors.Is(err, apperr.Forbidden):
		status = http.StatusForbidden
	case errors.Is(err, apperr.Locked):
		status = http.StatusLocked
	case errors.Is(err, apperr.Conflict):
		status = http.StatusConflict
	case errors.Is(err, apperr.Unavailable):
		status = http.StatusServiceUnavailable
	case errors.Is(err, apperr.Invalid):
		status = http.StatusBadRequest
	}
	msg := err.Error()
	if status == http.StatusInternalServerError {
		slog.Error("request failed", "err", err)
		msg = "internal error"
	} else if i := strings.LastIndex(msg, ": "); i > 0 {
		msg = msg[:i] // drop the trailing kind ("…: not found")
	}
	JSON(w, status, map[string]string{"error": msg})
}
