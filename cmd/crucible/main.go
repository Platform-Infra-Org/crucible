// Command crucible is the authoring and operations CLI.
package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"crucible/internal/config"
	"crucible/internal/content"
)

const usage = `usage:
  crucible lint <content-or-platform-dir>`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "lint":
		if len(os.Args) != 3 {
			fmt.Fprintln(os.Stderr, usage)
			os.Exit(2)
		}
		os.Exit(lint(os.Args[2], os.Stdout))
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
}

func lint(dir string, w io.Writer) int {
	if _, err := os.Stat(filepath.Join(dir, "platform.yaml")); err == nil {
		if _, err := config.Load(dir); err != nil {
			fmt.Fprintln(w, err)
			return 1
		}
		fmt.Fprintln(w, "platform config OK. The forge is ready.")
		return 0
	}
	t, probs := content.Load(dir)
	probs = append(probs, shellcheck(dir)...)
	for _, p := range probs {
		fmt.Fprintln(w, p)
	}
	if len(probs) > 0 {
		fmt.Fprintf(w, "%d problem(s). The metal isn't ready yet.\n", len(probs))
		return 1
	}
	fmt.Fprintf(w, "%s: %d module(s) OK. Ready for the forge.\n", t.ID, len(t.Modules))
	return 0
}

// shellcheck runs shellcheck on every *.sh file if it is installed (spec §6). Missing shellcheck is not an error.
func shellcheck(dir string) []content.Problem {
	if _, err := exec.LookPath("shellcheck"); err != nil {
		return nil
	}
	var probs []content.Problem
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".sh") {
			return err
		}
		if out, err := exec.Command("shellcheck", "-S", "warning", p).CombinedOutput(); err != nil {
			rel, _ := filepath.Rel(dir, p)
			probs = append(probs, content.Problem{File: rel, Msg: "shellcheck: " + strings.TrimSpace(string(out))})
		}
		return nil
	})
	return probs
}
