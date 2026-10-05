package auth

import (
	"encoding/json"
	"testing"
)

func TestClaimsRequireVerifiedEmail(t *testing.T) {
	cases := map[string]bool{
		`{"email":"a@b.c","email_verified":true}`:    true,
		`{"email":"a@b.c","email_verified":"true"}`:  true,
		`{"email":"a@b.c","email_verified":false}`:   false,
		`{"email":"a@b.c","email_verified":"false"}`: false,
		`{"email":"a@b.c"}`:                          false,
		`{"email_verified":true}`:                    false,
	}
	for raw, ok := range cases {
		var c idClaims
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			t.Fatal(err)
		}
		if got := c.problem() == ""; got != ok {
			t.Errorf("%s: allowed=%v, want %v (%q)", raw, got, ok, c.problem())
		}
	}
}
