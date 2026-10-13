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
	"slices"
	"strings"
	_ "time/tzdata" // schedules name IANA zones; the runtime image has no zoneinfo

	"crucible/internal/awsops"
	"crucible/internal/config"
	"crucible/internal/content"
)

const usage = `usage:
  crucible lint <content-or-platform-dir>
  crucible preview <content-dir> [--port 8090] [--image crucible:dev] [--free]
  crucible aws init --region REGION --domain HOSTNAME
  crucible aws labs-init --region REGION [--allowed-regions R1,R2] [--crucible-account ID] [--budget-usd N] [--budget-email ADDR]...
  crucible aws up [--var-file FILE] [--no-snapshot] [--no-labs]
  crucible aws deploy | snapshot | wake | status
  crucible aws sleep [--no-snapshot]
  crucible aws teardown [--var-file FILE] --yes [--no-snapshot]
  crucible local up [--keycloak-port PORT] [--no-build]
  crucible local down [--wipe]
  crucible local status

local runs the local stack (Postgres, Keycloak, Crucible) with Docker Compose from the repo root: up builds, seeds
the example training repos and starts it at http://localhost:8080; down stops it and keeps its data (--wipe deletes
the database and Keycloak data). --keycloak-port defaults to $KEYCLOAK_PORT, else 8081.

lint is offline: an aws lab whose resources Crucible can't price fails unless aws.prices declares their USD per
hour (aws.usage states monthly usage, such as storage_gb, for usage-priced resources).
labs-init --budget-usd and --budget-email set the lab account's monthly AWS Budget alert (a backstop; repeat --budget-email);
a re-run without them keeps the current budget. labs-init --allowed-regions (comma-separated, must include --region) sets
the regions labs may use; a re-run without it keeps the current list, and a new stack allows --region alone.
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
	case "preview":
		os.Exit(preview(os.Args[2:]))
	case "aws":
		os.Exit(awsCmd(os.Args[2:]))
	case "local":
		os.Exit(localCmd(os.Args[2:]))
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
	region := fs.String("region", "", "AWS region (init, labs-init)")
	domain := fs.String("domain", "", "public hostname, e.g. crucible.example.com (init)")
	varFile := fs.String("var-file", "", "terraform variables file (default <repo root>/deploy/aws/main/crucible.tfvars)")
	crucibleAccount := fs.String("crucible-account", "", "labs-init: account id that runs Crucible, when labs live in another account")
	yes := fs.Bool("yes", false, "confirm teardown")
	noSnapshot := fs.Bool("no-snapshot", false, "up/sleep/teardown: skip the safety snapshot (may lose data)")
	noLabs := fs.Bool("no-labs", false, "up: turn aws labs off (running aws labs lose credentials and cleanup)")
	budgetUSD := fs.Float64("budget-usd", 0, "labs-init: the lab account's monthly AWS Budget in USD (0: keep the current one, or the stack's default)")
	allowed := fs.String("allowed-regions", "", "labs-init: comma-separated regions labs may use, including --region (default: keep the current list, or --region alone)")
	var budgetEmails []string
	fs.Func("budget-email", "labs-init: who the AWS Budget alerts (repeat for more)", func(v string) error {
		budgetEmails = append(budgetEmails, v)
		return nil
	})
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
	case "labs-init":
		if *region == "" {
			fmt.Fprintln(os.Stderr, "--region is required")
			return 2
		}
		if *budgetUSD < 0 {
			fmt.Fprintln(os.Stderr, "--budget-usd must be positive (or leave it out for the stack's default)")
			return 2
		}
		var regions []string
		for _, r := range strings.Split(*allowed, ",") {
			if r = strings.TrimSpace(r); r != "" {
				regions = append(regions, r)
			}
		}
		if len(regions) > 0 && !slices.Contains(regions, *region) {
			fmt.Fprintf(os.Stderr, "--allowed-regions must include --region %s\n", *region)
			return 2
		}
		err = ops.LabsInit(ctx, *region, *crucibleAccount, *budgetUSD, budgetEmails, regions)
	case "up":
		err = ops.Up(ctx, *varFile, *noSnapshot, *noLabs)
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
