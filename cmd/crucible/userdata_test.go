package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var heredoc = regexp.MustCompile(`<<-?\s*'?"?(\w+)`)

// Terraform's <<- strips the common leading indent of the heredoc; a terminator indented deeper than the rest then
// reaches bash with spaces and never closes. Render each forge-402 user_data that way and syntax-check it.
func TestForge402UserDataParses(t *testing.T) {
	files, _ := filepath.Glob("../../examples/forge-402/modules/*/lab/terraform/main.tf")
	if len(files) != 2 {
		t.Fatalf("found %d main.tf files", len(files))
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(string(b), "\n")
		var body []string
		in := false
		for _, l := range lines {
			switch {
			case !in && strings.Contains(l, "user_data = <<-USERDATA"):
				in = true
			case in && strings.TrimSpace(l) == "USERDATA":
				in = false
			case in:
				body = append(body, l)
			}
		}
		if len(body) == 0 {
			t.Fatalf("%s: no user_data", f)
		}
		indent := -1
		for _, l := range body {
			if strings.TrimSpace(l) == "" {
				continue
			}
			if n := len(l) - len(strings.TrimLeft(l, " ")); indent < 0 || n < indent {
				indent = n
			}
		}
		for i, l := range body {
			if len(l) >= indent {
				body[i] = l[indent:]
			}
		}
		// bash -n (3.2 on a Mac) does not even warn about a heredoc that never closes, so check the terminators too.
		for _, m := range heredoc.FindAllStringSubmatch(strings.Join(body, "\n"), -1) {
			closed := false
			for _, l := range body {
				closed = closed || l == m[1]
			}
			if !closed {
				t.Errorf("%s: heredoc %s has no terminator line at column 0 after the <<- indent is stripped", f, m[1])
			}
		}
		cmd := exec.Command("bash", "-n")
		cmd.Stdin = strings.NewReader(strings.Join(body, "\n") + "\n")
		// bash -n only warns (exit 0) about a heredoc that runs to end of input, so any output is a failure.
		if out, err := cmd.CombinedOutput(); err != nil || len(out) > 0 {
			t.Errorf("%s: bash -n: %v\n%s", f, err, out)
		}
	}
}
