package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadExamplePlatform(t *testing.T) {
	p, err := Load("../../examples/platform")
	if err != nil {
		t.Fatal(err)
	}
	team := p.Teams["forge"]
	if team == nil || team.Name != "The Forge" {
		t.Fatalf("team not loaded: %+v", team)
	}
	if got := team.RoleOf("TRAINEE@crucible.local"); got != "trainee" {
		t.Fatalf("RoleOf = %q", got)
	}
	prog := team.Programs["forge-101"]
	if prog == nil {
		t.Fatal("program missing")
	}
	// Spec §5.2 defaults: leader → manager + approver, seniors → scorers.
	if prog.Roles.Manager[0] != "leader@crucible.local" || prog.Roles.Approvers[0] != "leader@crucible.local" {
		t.Fatalf("role defaults: %+v", prog.Roles)
	}
	if prog.Roles.Scorers[0] != "senior@crucible.local" {
		t.Fatalf("scorer default: %+v", prog.Roles)
	}
	if prog.LabDefaults.TTL.D() != 2*time.Hour {
		t.Fatalf("ttl %v", prog.LabDefaults.TTL.D())
	}
	if p.Settings.DefaultTheme != "forge" || len(p.Settings.Quotes) != 3 {
		t.Fatalf("settings %+v", p.Settings)
	}
}

func copyTree(t *testing.T, src string) string {
	dst := t.TempDir()
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if info.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

func TestLoadRejectsBadConfig(t *testing.T) {
	cases := map[string]struct{ file, body, want string }{
		"duplicate role": {"teams/forge/team.yaml",
			"name: F\nleader: a@x\nseniors: [a@x]\ntrainees: []\n", "more than one team role"},
		"unknown training": {"teams/forge/programs/forge-101.yaml",
			"training: forge-999\n", "must match the file name"},
		"enrolled outsider": {"teams/forge/programs/forge-101.yaml",
			"training: forge-101\nenrolled: [stranger@x]\n", "not a member of team"},
		"bad theme": {"platform.yaml", "default_theme: neon\n", "default_theme"},
		"unknown key": {"platform.yaml", "default_theme: forge\ncolour: red\n", "colour"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			dir := copyTree(t, "../../examples/platform")
			if err := os.WriteFile(filepath.Join(dir, c.file), []byte(c.body), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := Load(dir)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want error containing %q, got %v", c.want, err)
			}
		})
	}
}
