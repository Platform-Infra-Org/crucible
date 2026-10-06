package configapi

import (
	"context"
	"strings"
	"testing"
)

func TestSeedAdminOnlyIntoAnEmptyAdminsFile(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	if ok, err := SeedAdmin(ctx, f.s.Writer, "Boss@Example.com"); err != nil || ok {
		t.Fatalf("the examples platform already has admins: %v %v", ok, err)
	}
	f.push(t, map[string]string{"admins.yaml": "admins: []\n"})
	if ok, err := SeedAdmin(ctx, f.s.Writer, "Boss@Example.com"); err != nil || !ok {
		t.Fatalf("seed: %v %v", ok, err)
	}
	if got := sh(t, "", "--git-dir", f.remote, "show", "main:admins.yaml"); !strings.Contains(got, "boss@example.com") {
		t.Fatalf("admins.yaml:\n%s", got)
	}
	if ok, _ := SeedAdmin(ctx, f.s.Writer, "other@example.com"); ok {
		t.Fatal("never twice")
	}
	if _, err := SeedAdmin(ctx, f.s.Writer, "not an email"); err == nil {
		t.Fatal("must look like an email")
	}
}
