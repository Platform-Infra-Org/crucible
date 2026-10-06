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

func TestBootstrapAdminRunsOnce(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	f.push(t, map[string]string{"admins.yaml": "admins: []\n"})
	if ok, err := BootstrapAdmin(ctx, f.s.DB, f.s.Writer, "Boss@Example.com"); err != nil || !ok {
		t.Fatalf("seed: %v %v", ok, err)
	}
	var actor, target string
	if err := f.s.DB.QueryRow(ctx, `SELECT actor, target FROM audit_log WHERE action = 'admin.bootstrap'`).Scan(&actor, &target); err != nil ||
		actor != "bootstrap" || target != "boss@example.com" {
		t.Fatalf("audit: %q %q %v", actor, target, err)
	}
	f.push(t, map[string]string{"admins.yaml": "admins: []\n"}) // everyone removed on purpose
	if ok, err := BootstrapAdmin(ctx, f.s.DB, f.s.Writer, "boss@example.com"); err != nil || ok {
		t.Fatalf("a restart must not seed again: %v %v", ok, err)
	}
	if got := sh(t, "", "--git-dir", f.remote, "show", "main:admins.yaml"); strings.Contains(got, "boss@") {
		t.Fatalf("re-seeded:\n%s", got)
	}
}

func TestSeedAdminRefusesABrokenAdminsFile(t *testing.T) {
	f := setup(t)
	f.push(t, map[string]string{"admins.yaml": "admins: [unclosed\n"})
	if ok, err := SeedAdmin(context.Background(), f.s.Writer, "boss@example.com"); ok || err == nil {
		t.Fatalf("must refuse: %v %v", ok, err)
	}
	if got := sh(t, "", "--git-dir", f.remote, "show", "main:admins.yaml"); !strings.Contains(got, "unclosed") {
		t.Fatalf("overwrote the file:\n%s", got)
	}
}
