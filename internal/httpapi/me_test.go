package httpapi

import (
	"testing"

	"crucible/internal/config"
)

func TestCanScore(t *testing.T) {
	p, err := config.Load("../../examples/platform")
	if err != nil {
		t.Fatal(err)
	}
	for email, want := range map[string]bool{
		"senior@crucible.local":  true, // seniors are scorers by default
		"admin@crucible.local":   true,
		"trainee@crucible.local": false,
		"leader@crucible.local":  false,
	} {
		if got := canScore(p, email); got != want {
			t.Errorf("%s: %v", email, got)
		}
	}
}
