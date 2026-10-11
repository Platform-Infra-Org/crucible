// Command crucible-ready says whether a cluster lab's Helm releases are ready. It runs in the lab's toolbox:
//
//	crucible-ready [--json] <release>... | all
//
// all means every release of the lab ($LAB_RELEASES). It exits 1 when anything is not ready.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"crucible/internal/ready"
)

func main() {
	asJSON := flag.Bool("json", false, "print a JSON report")
	flag.Parse()
	names := flag.Args()
	if len(names) == 1 && names[0] == "all" {
		names = strings.Fields(os.Getenv("LAB_RELEASES"))
	}
	ns := os.Getenv("LAB_NAMESPACE")
	if len(names) == 0 || ns == "" {
		fmt.Fprintln(os.Stderr, "usage: crucible-ready [--json] <release>... | all (needs LAB_NAMESPACE)")
		os.Exit(2)
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(clientcmd.NewDefaultClientConfigLoadingRules(), nil).ClientConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "crucible-ready:", err)
		os.Exit(2)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "crucible-ready:", err)
		os.Exit(2)
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
		_ = json.NewEncoder(os.Stdout).Encode(rep)
	} else {
		for _, r := range rep.Releases {
			fmt.Printf("%s: %s\n", r.Name, r.Status)
			for _, u := range r.Unready {
				fmt.Println("  " + u)
			}
		}
	}
	if !ok {
		os.Exit(1)
	}
}
