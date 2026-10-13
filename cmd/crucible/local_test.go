package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// repoWithCompose is a minimal repo root: local refuses to run anywhere without the compose file.
func repoWithCompose(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "deploy", "compose"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, localCompose), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func cmds(steps []step) []string {
	var out []string
	for _, s := range steps {
		out = append(out, strings.TrimSpace(s.name+" "+strings.Join(s.args, " ")))
	}
	return out
}

func TestLocalUpBuildsSeedsAndStartsTheStack(t *testing.T) {
	root := repoWithCompose(t)
	steps, err := localPlan(root, "up", localOpts{keycloakPort: "8082"}, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"make build",
		"make toolbox",
		"./scripts/seed-git.sh",
		"docker compose -f deploy/compose/docker-compose.yml up -d --build --wait",
	}
	if got := cmds(steps); !reflect.DeepEqual(got, want) {
		t.Fatalf("up runs\n%q\nwant\n%q", got, want)
	}
	for _, s := range steps {
		if !contains(s.env, "KEYCLOAK_PORT=8082") {
			t.Fatalf("%s %v runs without KEYCLOAK_PORT=8082: %v", s.name, s.args, s.env)
		}
	}
}

func TestLocalUpNoBuildSkipsMakeBuild(t *testing.T) {
	steps, err := localPlan(repoWithCompose(t), "up", localOpts{noBuild: true}, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if got := cmds(steps); got[0] != "make toolbox" {
		t.Fatalf("--no-build still builds: %q", got)
	}
}

// down keeps the data unless asked; --wipe is the only way to delete volumes, and only this project's.
func TestLocalDownKeepsDataUnlessWiped(t *testing.T) {
	root := repoWithCompose(t)
	env := func(string) string { return "" }
	keep, _ := localPlan(root, "down", localOpts{}, env)
	wipe, _ := localPlan(root, "down", localOpts{wipe: true}, env)
	if got := cmds(keep); !reflect.DeepEqual(got, []string{"docker compose -f deploy/compose/docker-compose.yml down"}) {
		t.Fatalf("down runs %q", got)
	}
	if got := cmds(wipe); !reflect.DeepEqual(got, []string{"docker compose -f deploy/compose/docker-compose.yml down -v"}) {
		t.Fatalf("down --wipe runs %q", got)
	}
}

func TestLocalStatusListsTheStack(t *testing.T) {
	steps, err := localPlan(repoWithCompose(t), "status", localOpts{}, func(string) string { return "" })
	if err != nil || !reflect.DeepEqual(cmds(steps), []string{"docker compose -f deploy/compose/docker-compose.yml ps"}) {
		t.Fatalf("status runs %q, %v", cmds(steps), err)
	}
}

// The flag wins over $KEYCLOAK_PORT, which wins over compose's own default (no variable set at all).
func TestLocalKeycloakPortPrecedence(t *testing.T) {
	root := repoWithCompose(t)
	fromEnv := func(k string) string {
		if k == "KEYCLOAK_PORT" {
			return "9090"
		}
		return ""
	}
	flag, _ := localPlan(root, "status", localOpts{keycloakPort: "8082"}, fromEnv)
	env, _ := localPlan(root, "status", localOpts{}, fromEnv)
	none, _ := localPlan(root, "status", localOpts{}, func(string) string { return "" })
	if !contains(flag[0].env, "KEYCLOAK_PORT=8082") || !contains(env[0].env, "KEYCLOAK_PORT=9090") {
		t.Fatalf("port precedence: flag %v, env %v", flag[0].env, env[0].env)
	}
	for _, e := range none[0].env {
		if strings.HasPrefix(e, "KEYCLOAK_PORT=") {
			t.Fatalf("no port given, yet %s is set: compose's default must apply", e)
		}
	}
}

func TestLocalRefusesOutsideTheRepo(t *testing.T) {
	if _, err := localPlan(t.TempDir(), "up", localOpts{}, func(string) string { return "" }); err == nil {
		t.Fatal("a directory without deploy/compose was accepted")
	}
	if _, err := localPlan(repoWithCompose(t), "restart", localOpts{}, func(string) string { return "" }); err == nil {
		t.Fatal("an unknown subcommand was accepted")
	}
}

// The repo's own Go and Terraform come first on PATH when they exist, as CLAUDE.md asks of every build.
func TestLocalPutsTheRepoToolchainOnPath(t *testing.T) {
	root := repoWithCompose(t)
	if err := os.MkdirAll(filepath.Join(root, ".local", "tools", "go", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	steps, _ := localPlan(root, "up", localOpts{}, func(k string) string {
		if k == "PATH" {
			return "/usr/bin"
		}
		return ""
	})
	want := "PATH=" + filepath.Join(root, ".local", "tools", "go", "bin") + string(os.PathListSeparator) +
		filepath.Join(root, ".local", "tools") + string(os.PathListSeparator) + "/usr/bin"
	if !contains(steps[0].env, want) {
		t.Fatalf("PATH not prefixed with the repo toolchain: %v", steps[0].env)
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
