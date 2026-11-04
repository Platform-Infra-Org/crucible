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
	"crucible/internal/docs"
)

const usage = `usage:
  crucible serve
  crucible version
  crucible lint <content-or-platform-dir>
  crucible preview <content-dir> [--port 8090] [--image crucible:dev] [--free]
  crucible aws init --region REGION (--domain HOSTNAME | --access ssm --subnet-id SUBNET [--local-port 18080])
  crucible aws labs-init --region REGION [--allowed-regions R1,R2] [--crucible-account ID] [--budget-usd N] [--budget-email ADDR]... [--vpc-id VPC --subnet-ids S1,S2]
  crucible aws up [--var-file FILE] [--no-snapshot] [--no-labs]
  crucible aws deploy | snapshot | wake | status
  crucible aws connect [--region REGION] [--port PORT] [--name crucible]
  crucible aws invite EMAIL [--name "Full Name"]
  crucible aws sleep [--no-snapshot]
  crucible aws teardown [--var-file FILE] --yes [--no-snapshot]
  crucible local up [--keycloak-port PORT] [--no-build]
  crucible local down [--wipe]
  crucible local status

serve runs the server; it is configured by environment variables only. version prints the build version.

local runs the local stack (Postgres, Keycloak, Crucible) with Docker Compose from the repo root: up builds, seeds
the example training repos and starts it at http://localhost:8080; down stops it and keeps its data (--wipe deletes
the database and Keycloak data). --keycloak-port defaults to $KEYCLOAK_PORT, else 8081.

lint is offline: an aws lab whose resources Crucible can't price fails unless aws.prices declares their USD per
hour (aws.usage states monthly usage, such as storage_gb, for usage-priced resources).
init --access ssm makes an install with no way in: no domain, no Elastic IP, no Let's Encrypt; everyone reaches it at
http://localhost:<--local-port> through ` + "`crucible aws connect`" + `. A re-run without --access, --domain or --local-port
keeps the current ones. labs-init --vpc-id and --subnet-ids (both or neither) are where lab modules launch, for an
account without a default VPC; a re-run without them keeps the current ones.
invite adds someone to the Cognito user pool with a verified email (Crucible refuses unverified ones); run it again to
resend an invite that has not been used yet.
connect opens the tunnel to an --access ssm install (AWS CLI, Session Manager plugin and your SSO login needed) and keeps it open until Ctrl-C.
labs-init --budget-usd and --budget-email set the lab account's monthly AWS Budget alert (a backstop; repeat --budget-email);
a re-run without them keeps the current budget. labs-init --allowed-regions (comma-separated, must include --region) sets
the regions labs may use; a re-run without it keeps the current list, and a new stack allows --region alone.
--var-file defaults to deploy/aws/main/crucible.tfvars in the repo root.
--no-snapshot skips the safety snapshot: changes since the last backup may be lost.`

func main() { os.Exit(runMain(os.Args[1:], os.Stdout, os.Stderr)) }

func runMain(args []string, out, errw io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(errw, usage)
		return 2
	}
	switch args[0] {
	case "serve":
		return serve(args[1:])
	case "version":
		fmt.Fprintln(out, docs.Version)
		return 0
	case "lint":
		if len(args) != 2 {
			fmt.Fprintln(errw, usage)
			return 2
		}
		return lint(args[1], out)
	case "preview":
		return preview(args[1:])
	case "aws":
		return awsCmd(args[1:])
	case "local":
		return localCmd(args[1:])
	default:
		fmt.Fprintln(errw, usage)
		return 2
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
	access := fs.String("access", "", "init: public (a domain, an Elastic IP, Let's Encrypt) or ssm (no way in; `crucible aws connect`); default: keep the current one, else public")
	subnetID := fs.String("subnet-id", "", "init, access ssm: the existing subnet the node runs in (default: keep the current one)")
	localPort := fs.Int("local-port", 0, "init, access ssm: the laptop port everyone opens Crucible on (default: keep the current one, else 18080)")
	vpcID := fs.String("vpc-id", "", "labs-init: the VPC lab modules launch into (var.crucible_vpc_id), for an account without a default VPC")
	subnetIDs := fs.String("subnet-ids", "", "labs-init: comma-separated subnets in that VPC (var.crucible_subnet_ids)")
	name := fs.String("name", "", "connect: the node's Name tag (default crucible); invite: the person's full name")
	port := fs.Int("port", 0, "connect: the local port (default: the one in Crucible's address; another one breaks sign-in)")
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
	rest := args[1:]
	if args[0] == "invite" && len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		rest = append(rest[1:], rest[0]) // invite EMAIL --name …: flags stop at the first plain argument
	}
	_ = fs.Parse(rest)
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
		if *region == "" {
			fmt.Fprintln(os.Stderr, "--region is required")
			return 2
		}
		err = ops.Init(ctx, awsops.InitOpts{Region: *region, Domain: *domain, Access: *access, SubnetID: *subnetID, LocalPort: *localPort})
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
		var subnets []string
		for _, s := range strings.Split(*subnetIDs, ",") {
			if s = strings.TrimSpace(s); s != "" {
				subnets = append(subnets, s)
			}
		}
		err = ops.LabsInit(ctx, *region, *crucibleAccount, *budgetUSD, budgetEmails, regions, awsops.LabNet{VPCID: *vpcID, SubnetIDs: subnets})
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
	case "connect":
		err = ops.Connect(ctx, awsops.ConnectOpts{Region: *region, Name: *name, Port: *port})
	case "invite":
		if fs.NArg() != 1 {
			fmt.Fprintln(os.Stderr, `usage: crucible aws invite EMAIL [--name "Full Name"]`)
			return 2
		}
		err = ops.Invite(ctx, fs.Arg(0), *name)
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
