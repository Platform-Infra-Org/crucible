// This file is `crucible ready`. It says whether a cluster lab's Helm releases are ready. It runs in the lab's toolbox:
//
//	crucible ready [--json] <release>... | all
//
// all means every release of the lab ($LAB_RELEASES). It exits 1 when anything is not ready.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"crucible/internal/ready"
)

func readyCmd(args []string, out, errw io.Writer) int {
	fs := flag.NewFlagSet("ready", flag.ContinueOnError)
	fs.SetOutput(errw)
	asJSON := fs.Bool("json", false, "print a JSON report")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	names := fs.Args()
	if len(names) == 1 && names[0] == "all" {
		names = strings.Fields(os.Getenv("LAB_RELEASES"))
	}
	ns := os.Getenv("LAB_NAMESPACE")
	if len(names) == 0 || ns == "" {
		fmt.Fprintln(errw, "usage: crucible ready [--json] <release>... | all (needs LAB_NAMESPACE)")
		return 2
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(clientcmd.NewDefaultClientConfigLoadingRules(), nil).ClientConfig()
	if err != nil {
		fmt.Fprintln(errw, "crucible ready:", err)
		return 2
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		fmt.Fprintln(errw, "crucible ready:", err)
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	rep := ready.Report{Releases: []ready.Release{}}
	for _, n := range names {
		out, err := exec.CommandContext(ctx, "helm", "get", "manifest", n, "-n", ns).Output()
		if err != nil {
			rep.Releases = append(rep.Releases, ready.Release{Name: n, Status: ready.Failing, Unready: []string{"release not installed"}})
			continue
		}
		rep.Releases = append(rep.Releases, ready.Judge(ctx, cs, ns, n, ready.Parse(out)))
	}
	ok := true
	for _, r := range rep.Releases {
		ok = ok && r.Status == ready.Healthy
	}
	if *asJSON {
		_ = json.NewEncoder(out).Encode(rep)
	} else {
		for _, r := range rep.Releases {
			fmt.Fprintf(out, "%s: %s\n", r.Name, r.Status)
			for _, u := range r.Unready {
				fmt.Fprintln(out, "  "+u)
			}
		}
	}
	if !ok {
		return 1
	}
	return 0
}
