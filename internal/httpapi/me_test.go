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
		"senior@crucible.local":  true, // the seed's editors (forge_leaders, forge_seniors) score
		"leader@crucible.local":  true,
		"admin@crucible.local":   true,
		"trainee@crucible.local": false,
		"nobody@crucible.local":  false,
	} {
		if got := canScore(p, email); got != want {
			t.Errorf("%s: %v", email, got)
		}
	}
}
