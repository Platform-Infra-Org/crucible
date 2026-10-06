package configapi

import (
	"context"
	"errors"
	"strings"

	"crucible/internal/config"
	"crucible/internal/gitsync"
)

var errSeeded = errors.New("admins already set")

// SeedAdmin writes the bootstrap admin into admins.yaml when the platform repo names no admin yet (spec §5.1: the Helm
// value exists only to seed admins.yaml on first start). A repo that already has an admin is never touched, so git stays
// the source of truth: afterwards the bootstrap value changes nothing, and removing the admin in git removes them.
func SeedAdmin(ctx context.Context, w *gitsync.Writer, email string) (bool, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if err := checkEmail("bootstrap admin", email); err != nil {
		return false, err
	}
	_, changed, err := w.Apply(ctx, gitsync.Change{Action: "seed the bootstrap admin", Actor: email, Paths: []string{"admins.yaml"},
		Allow: func(p *config.Platform) error {
			if len(p.Admins) > 0 {
				return errSeeded
			}
			return nil
		},
		Edit: edit("admins.yaml", map[string]any{"admins": []string{email}})})
	if errors.Is(err, errSeeded) {
		return false, nil
	}
	return changed, err
}
