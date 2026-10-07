// Package httpx holds the JSON and error helpers every handler uses.
package httpx

import (
	"bytes"
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

const maxBody = 1 << 20

// Read decodes a JSON body of at most 1 MiB and rejects unknown fields. A larger body is refused as such, never
// truncated into a confusing parse error.
func Read(r *http.Request, v any) error {
	b, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		return apperr.Wrap(apperr.Invalid, err.Error())
	}
	if len(b) > maxBody {
		return apperr.Wrap(apperr.Invalid, "the request is over 1 MiB; make it smaller")
	}
	dec := json.NewDecoder(bytes.NewReader(b))
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
