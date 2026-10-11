package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ap "crucible/internal/agentproto"
)

const twoContexts = `apiVersion: v1
kind: Config
clusters:
  - name: kind
    cluster: {server: "https://127.0.0.1:52011", certificate-authority-data: "c2VjcmV0"}
  - name: eks
    cluster: {server: "https://ABC.gr7.eu-west-1.eks.amazonaws.com"}
contexts:
  - {name: kind-dev, context: {cluster: kind, user: me}}
  - {name: prod-eks, context: {cluster: eks, user: me}}
users:
  - name: me
    user: {token: s3cr3t-token}
`

func TestReadContextsReportsNamesAndHostsOnly(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config")
	_ = os.WriteFile(p, []byte(twoContexts), 0o600)
	got := ReadContexts(p)
	if len(got) != 2 || got[0].Name != "kind-dev" || got[0].Server != "127.0.0.1:52011" || got[1].Server != "ABC.gr7.eu-west-1.eks.amazonaws.com" {
		t.Fatalf("%+v", got)
	}
	if s := strings.ToLower(fmtContexts(got)); strings.Contains(s, "s3cr3t") || strings.Contains(s, "c2vjcmv0") {
		t.Fatal("credentials leaked")
	}
}

func TestReadContextsSurvivesMissingAndBrokenFiles(t *testing.T) {
	dir := t.TempDir()
	if got := ReadContexts(filepath.Join(dir, "nope")); len(got) != 0 {
		t.Fatalf("missing: %+v", got)
	}
	broken := filepath.Join(dir, "broken")
	_ = os.WriteFile(broken, []byte("contexts: [\n"), 0o600)
	if got := ReadContexts(broken); len(got) != 0 {
		t.Fatalf("broken: %+v", got)
	}
}

func TestWatcherSendsChanges(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config")
	_ = os.WriteFile(p, []byte(twoContexts), 0o600)
	w := &Watcher{Kubeconfig: p, Every: 20 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)
	if got := <-w.Changes(); len(got) != 2 {
		t.Fatalf("first: %+v", got)
	}
	_ = os.WriteFile(p, []byte(strings.Replace(twoContexts, "prod-eks", "staging", 2)), 0o600)
	select {
	case got := <-w.Changes():
		if got[1].Name != "staging" || len(w.Current()) != 2 {
			t.Fatalf("change: %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no change sent")
	}
}

func fmtContexts(cs []ap.KubeContext) string {
	var b strings.Builder
	for _, c := range cs {
		b.WriteString(c.Name + " " + c.Server + "\n")
	}
	return b.String()
}

func TestLoadingRulesAlsoReadCrucibleKubeconfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KUBECONFIG", "")
	_ = os.Unsetenv("KUBECONFIG")
	_ = os.MkdirAll(filepath.Join(home, ".kube"), 0o700)
	_ = os.MkdirAll(filepath.Join(home, ".crucible"), 0o700)
	_ = os.WriteFile(filepath.Join(home, ".kube", "config"), []byte(twoContexts), 0o600)
	own := strings.NewReplacer("kind-dev", "kind-crucible", "prod-eks", "other").Replace(twoContexts)
	_ = os.WriteFile(filepath.Join(home, ".crucible", "kubeconfig"), []byte(own), 0o600)

	names := func(cs []ap.KubeContext) string {
		s := []string{}
		for _, c := range cs {
			s = append(s, c.Name)
		}
		return strings.Join(s, ",")
	}
	if got := names(ReadContexts("")); got != "kind-crucible,kind-dev,other,prod-eks" {
		t.Fatalf("both files: %s", got)
	}
	if got := names(ReadContexts(filepath.Join(home, ".kube", "config"))); got != "kind-dev,prod-eks" {
		t.Fatalf("explicit: %s", got)
	}
}
