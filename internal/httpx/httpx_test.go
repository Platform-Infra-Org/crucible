package httpx

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"crucible/internal/apperr"
)

func TestErrorMapsKinds(t *testing.T) {
	cases := map[error]int{
		apperr.Wrap(apperr.NotFound, "no lab"):      404,
		apperr.Wrap(apperr.Forbidden, "nope"):       403,
		apperr.Wrap(apperr.Locked, "finish first"):  423,
		apperr.Wrap(apperr.Conflict, "busy"):        409,
		apperr.Wrap(apperr.Unavailable, "offline"):  503,
		apperr.Wrap(apperr.Invalid, "bad json"):     400,
		errors.New("database exploded"):             500,
	}
	for err, want := range cases {
		w := httptest.NewRecorder()
		Error(w, err)
		if w.Code != want {
			t.Errorf("%v: got %d want %d", err, w.Code, want)
		}
	}
}

func TestErrorHidesInternalMessages(t *testing.T) {
	w := httptest.NewRecorder()
	Error(w, errors.New("password=hunter2"))
	if strings.Contains(w.Body.String(), "hunter2") {
		t.Fatalf("internal error leaked: %s", w.Body.String())
	}
}
