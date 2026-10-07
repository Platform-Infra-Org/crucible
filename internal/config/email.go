package config

import (
	"fmt"
	"strings"
	"unicode"

	"crucible/internal/apperr"
)

// CheckEmail is the one email-shape check shared by configapi and org.
func CheckEmail(field, e string) error {
	if !strings.Contains(e, "@") || strings.ContainsFunc(e, unicode.IsSpace) {
		return apperr.Wrap(apperr.Invalid, fmt.Sprintf("%s: %q is not an email address", field, e))
	}
	return nil
}
