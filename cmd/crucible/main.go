// Command crucible is the authoring and operations CLI.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"time"
	_ "time/tzdata" // schedules name IANA zones; the runtime image has no zoneinfo

	"crucible/internal/awsops"
	"crucible/internal/config"
	"crucible/internal/content"
	"crucible/internal/infracost"
)

const usage = `usage:
  crucible lint <content-or-platform-dir>
  crucible aws init --region REGION --domain HOSTNAME
  crucible aws up [--var-file FILE] [--no-snapshot]
  crucible aws deploy | snapshot | wake | status
  crucible aws sleep [--no-snapshot]
  crucible aws teardown [--var-file FILE] --yes [--no-snapshot]

--var-file defaults to deploy/aws/main/crucible.tfvars in the repo root.
--no-snapshot skips the safety snapshot: changes since the last backup may be lost.`

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
	case "aws":
		os.Exit(awsCmd(os.Args[2:]))
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
}

func awsCmd(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	fs := flag.NewFlagSet("aws "+args[0], flag.ExitOnError)
	region := fs.String("region", "", "AWS region (init)")
	domain := fs.String("domain", "", "public hostname, e.g. crucible.example.com (init)")
	varFile := fs.String("var-file", "", "terraform variables file (default <repo root>/deploy/aws/main/crucible.tfvars)")
	yes := fs.Bool("yes", false, "confirm teardown")
	noSnapshot := fs.Bool("no-snapshot", false, "up/sleep/teardown: skip the safety snapshot (may lose data)")
	_ = fs.Parse(args[1:])
	root, _ := os.Getwd()
	if top, err := exec.Command("git", "rev-parse", "--show-toplevel").Output(); err == nil {
		root = strings.TrimSpace(string(top))
	}
	if *varFile == "" {
		*varFile = filepath.Join(root, "deploy", "aws", "main", "crucible.tfvars")
	}
	ops := awsops.Default(root)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	var err error
	switch args[0] {
	case "init":
		if *region == "" || *domain == "" {
			fmt.Fprintln(os.Stderr, "--region and --domain are required")
			return 2
		}
		err = ops.Init(ctx, *region, *domain)
	case "up":
		err = ops.Up(ctx, *varFile, *noSnapshot)
	case "deploy":
		err = ops.Deploy(ctx)
	case "snapshot":
		err = ops.Snapshot(ctx)
	case "sleep":
		err = ops.SleepNode(ctx, *noSnapshot)
	case "wake":
		err = ops.Wake(ctx)
	case "status":
		err = ops.Status(ctx)
	case "teardown":
		err = ops.Teardown(ctx, *varFile, *yes, *noSnapshot)
	default:
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
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
	probs = append(probs, priceCheck(t, w, priceRunner())...)
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

// priceRunner is the real infracost CLI when it is installed and INFRACOST_API_KEY is set, else nil.
func priceRunner() infracost.Runner {
	if _, err := exec.LookPath("infracost"); err != nil || os.Getenv("INFRACOST_API_KEY") == "" {
		return nil
	}
	return infracost.Exec
}

// priceCheck fails aws labs whose infracost estimate exceeds aws.max_hourly_usd (spec §4.5). Without a runner (no CLI
// or no key) it says so and checks nothing. t is nil when content failed to load.
func priceCheck(t *content.Training, w io.Writer, run infracost.Runner) []content.Problem {
	if t == nil {
		return nil
	}
	var probs []content.Problem
	for _, m := range t.Modules {
		if m.Lab == nil || m.Lab.Runtime != "aws" || m.Lab.AWS == nil {
			continue
		}
		if run == nil {
			fmt.Fprintf(w, "note: %s: price check skipped (needs the infracost CLI and INFRACOST_API_KEY)\n", m.Lab.ID)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		h, err := infracost.Hourly(ctx, run, filepath.Join(m.Lab.Dir, "terraform"), m.Lab.AWS.Region)
		cancel()
		switch {
		case err != nil:
			probs = append(probs, content.Problem{File: m.Lab.ID, Msg: "infracost: " + err.Error()})
		case h > m.Lab.AWS.MaxHourlyUSD:
			probs = append(probs, content.Problem{File: m.Lab.ID,
				Msg: fmt.Sprintf("infracost prices this lab at $%.4f/h, above aws.max_hourly_usd $%.2f", h, m.Lab.AWS.MaxHourlyUSD)})
		}
	}
	return probs
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
