// Package apperr defines error kinds that services return and HTTP maps to status codes.
package apperr

import (
	"errors"
	"fmt"
)

var (
	NotFound    = errors.New("not found")
	Forbidden   = errors.New("forbidden")
	Locked      = errors.New("locked")
	Conflict    = errors.New("conflict")
	Unavailable = errors.New("unavailable")
	Invalid     = errors.New("invalid")
)

// Wrap attaches a user-facing message to an error kind.
func Wrap(kind error, msg string) error { return fmt.Errorf("%s: %w", msg, kind) }
