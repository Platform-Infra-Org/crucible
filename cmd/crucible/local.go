package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
)

// localCompose is the local stack (Postgres, Keycloak, crucible-api) as the repo defines it. Its compose project is
// crucible-local, so down never touches another project's containers or volumes.
const localCompose = "deploy/compose/docker-compose.yml"

type localOpts struct {
	keycloakPort string
	noBuild      bool
	wipe         bool
}

// step is one command run from the repo root with env added to the caller's environment.
type step struct {
	name string
	args []string
	env  []string
}

// localPlan says what `crucible local <sub>` runs. getenv is os.Getenv in real use; tests pass their own.
func localPlan(root, sub string, o localOpts, getenv func(string) string) ([]step, error) {
	if _, err := os.Stat(filepath.Join(root, localCompose)); err != nil {
		return nil, fmt.Errorf("run this from the Crucible repo: %s not found under %s", localCompose, root)
	}
	var env []string
	if port := firstNonEmpty(o.keycloakPort, getenv("KEYCLOAK_PORT")); port != "" {
		env = append(env, "KEYCLOAK_PORT="+port) // unset: compose's own default, 8081
	}
	// The system Go and Terraform are too old; the repo brings its own (CLAUDE.md), so put them first when present.
	if goBin := filepath.Join(root, ".local", "tools", "go", "bin"); dirExists(goBin) {
		env = append(env, "PATH="+strings.Join([]string{goBin, filepath.Join(root, ".local", "tools"), getenv("PATH")}, string(os.PathListSeparator)))
	}
	compose := func(args ...string) step {
		return step{"docker", append([]string{"compose", "-f", localCompose}, args...), env}
	}
	switch sub {
	case "up":
		var steps []step
		if !o.noBuild {
			steps = append(steps, step{"make", []string{"build"}, env})
		}
		return append(steps,
			step{"make", []string{"toolbox"}, env}, // cluster labs run in it on trainees' clusters
			step{"./scripts/seed-git.sh", nil, env},
			compose("up", "-d", "--build", "--wait")), nil
	case "down":
		if o.wipe {
			return []step{compose("down", "-v")}, nil
		}
		return []step{compose("down")}, nil
	case "status":
		return []step{compose("ps")}, nil
	}
	return nil, errors.New("unknown local command " + sub)
}

func localCmd(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	fs := flag.NewFlagSet("local "+args[0], flag.ExitOnError)
	var o localOpts
	fs.StringVar(&o.keycloakPort, "keycloak-port", "", "up/status: host port for Keycloak (default $KEYCLOAK_PORT, else 8081)")
	fs.BoolVar(&o.noBuild, "no-build", false, "up: skip make build (use the binaries already in bin/)")
	fs.BoolVar(&o.wipe, "wipe", false, "down: also delete the stack's volumes (the database and Keycloak data)")
	_ = fs.Parse(args[1:])
	root, _ := os.Getwd()
	if top, err := exec.Command("git", "rev-parse", "--show-toplevel").Output(); err == nil {
		root = strings.TrimSpace(string(top))
	}
	steps, err := localPlan(root, args[0], o, os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	for _, s := range steps {
		fmt.Fprintf(os.Stderr, "== %s %s\n", s.name, strings.Join(s.args, " "))
		cmd := exec.CommandContext(ctx, s.name, s.args...)
		cmd.Dir, cmd.Stdout, cmd.Stderr, cmd.Env = root, os.Stdout, os.Stderr, append(os.Environ(), s.env...)
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "error: %s %s: %v\n", s.name, strings.Join(s.args, " "), err)
			return 1
		}
	}
	if args[0] == "up" {
		port := firstNonEmpty(o.keycloakPort, os.Getenv("KEYCLOAK_PORT"), "8081")
		fmt.Printf("\nCrucible is up at http://localhost:8080 (sign-in on port %s).\n"+
			"Users: trainee, senior, leader, admin; each password is the username.\n"+
			"Stop it with `crucible local down` (add --wipe to delete its data).\n", port)
	}
	return 0
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
