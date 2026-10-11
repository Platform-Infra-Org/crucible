package agent

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"k8s.io/client-go/discovery"
	"k8s.io/client-go/tools/clientcmd"

	ap "crucible/internal/agentproto"
)

// fakeDocker records docker calls and answers helm template with whatever the test set.
type fakeDocker struct {
	mu       sync.Mutex
	calls    []string
	template string
	failOn   string // a substring: the call containing it fails
}

func (f *fakeDocker) run(_ context.Context, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	call := strings.Join(args, " ")
	f.calls = append(f.calls, call)
	if f.failOn != "" && strings.Contains(call, f.failOn) {
		return []byte("Error: context deadline exceeded"), errors.New("exit status 1")
	}
	if strings.Contains(call, "helm template") {
		return []byte(f.template), nil
	}
	return nil, nil
}

func (f *fakeDocker) joined() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.calls, "\n")
}

func chartBundle(t *testing.T) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range map[string]string{"charts/shop/Chart.yaml": "name: shop\n", "values/shop.yaml": "x: 1\n"} {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(body))
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func testCluster(t *testing.T, f *fakeDocker) *Cluster {
	srv, _ := upstream(t)
	return &Cluster{Dir: t.TempDir(), Kubeconfig: kubeconfigFor(t, srv, "{token: real}"), Run: f.run,
		Bind: func(context.Context) (string, error) { return "127.0.0.1", nil }}
}

var spec = ap.ClusterSpec{Context: "lab", Toolbox: "toolbox@sha256:abc", Charts: []ap.Chart{
	{Name: "shop", Path: "charts/shop", Values: "values/shop.yaml", TimeoutS: 300}, {Name: "db", Path: "charts/shop", TimeoutS: 60}}}

func TestProvisionInstallsInOrderThroughTheToolbox(t *testing.T) {
	f := &fakeDocker{template: "kind: Deployment\n"}
	c := testCluster(t, f)
	id := "0123456789ab"
	if err := c.Provision(context.Background(), id, chartBundle(t), spec); err != nil {
		t.Fatal(err)
	}
	calls := f.joined()
	for _, want := range []string{
		"run -d --name crucible-lab-0123456789ab --label crucible.lab=0123456789ab",
		"-e LAB_NAMESPACE=lab-0123456789ab", "-e LAB_RELEASES=shop db", "--add-host host.docker.internal:host-gateway",
		"toolbox@sha256:abc",
		"exec crucible-lab-0123456789ab sh -c kubectl create namespace lab-0123456789ab",
		"helm upgrade --install shop /tmp/charts/shop -n lab-0123456789ab -f /lab/values/shop.yaml --wait --timeout 300s",
	} {
		if !strings.Contains(calls, want) {
			t.Errorf("missing %q in\n%s", want, calls)
		}
	}
	if strings.Index(calls, "helm upgrade --install shop") > strings.Index(calls, "helm upgrade --install db") {
		t.Error("charts install in order")
	}
	kc, err := os.ReadFile(filepath.Join(c.Dir, id, "kubeconfig"))
	if err != nil || strings.Contains(string(kc), "real") || !strings.Contains(string(kc), "https://host.docker.internal:") || !strings.Contains(string(kc), "namespace: lab-0123456789ab") {
		t.Fatalf("toolbox kubeconfig: %s %v", kc, err)
	}
	if st, _ := os.Stat(filepath.Join(c.Dir, id, "kubeconfig")); st.Mode().Perm() != 0o600 {
		t.Fatalf("kubeconfig mode %v", st.Mode())
	}
	if got := c.Labs(); len(got) != 1 || got[0] != id {
		t.Fatalf("Labs %v", got)
	}
}

// kubectl and helm in the toolbox read this kubeconfig with clientcmd, which sends a user's token only over TLS: over
// plain http every call was 401 "You must be logged in to the server" (the first e2e on kind).
func TestToolboxKubeconfigReachesTheClusterThroughTheProxy(t *testing.T) {
	srv, seen := upstream(t)
	c := &Cluster{Dir: t.TempDir(), Kubeconfig: kubeconfigFor(t, srv, "{token: real}"), Bind: func(context.Context) (string, error) { return "127.0.0.1", nil }}
	id := "0123456789ab"
	_ = os.MkdirAll(filepath.Join(c.Dir, id), 0o755)
	if err := c.openProxy(context.Background(), id, spec); err != nil {
		t.Fatal(err)
	}
	defer c.closeProxy(id)
	// Dial 127.0.0.1 (what the name resolves to here) but verify the certificate as the toolbox does, for
	// host.docker.internal, against the kubeconfig's certificate-authority-data.
	kc := strings.Replace(readKubeconfig(t, c, id), `{server: "https://host.docker.internal:`, `{tls-server-name: host.docker.internal, server: "https://127.0.0.1:`, 1)
	if !strings.Contains(kc, "tls-server-name") || !strings.Contains(kc, "certificate-authority-data: ") {
		t.Fatalf("kubeconfig: %s", kc)
	}
	rc, err := clientcmd.RESTConfigFromKubeConfig([]byte(kc))
	if err != nil {
		t.Fatal(err)
	}
	dc, err := discovery.NewDiscoveryClientForConfig(rc)
	if err != nil {
		t.Fatal(err)
	}
	*seen = "" // the proxy's own probe at start
	if _, err := dc.ServerVersion(); err != nil || *seen != "Bearer real" {
		t.Fatalf("through the proxy: %v, upstream saw %q", err, *seen)
	}
}

func TestUndeclaredClusterWideKindsAreRefused(t *testing.T) {
	f := &fakeDocker{template: "---\nkind: ClusterRole\nmetadata: {name: r}\n"}
	err := testCluster(t, f).Provision(context.Background(), "0123456789ab", chartBundle(t), spec)
	if err == nil || !strings.Contains(err.Error(), "creates a ClusterRole") || strings.Contains(f.joined(), "helm upgrade") {
		t.Fatalf("err %v\n%s", err, f.joined())
	}
	wide := spec
	wide.ClusterWide = true
	if err := testCluster(t, &fakeDocker{template: "kind: ClusterRole\n"}).Provision(context.Background(), "0123456789ab", chartBundle(t), wide); err != nil {
		t.Fatalf("declared: %v", err)
	}
}

func TestFailedInstallCleansUp(t *testing.T) {
	f := &fakeDocker{template: "kind: Deployment\n", failOn: "helm upgrade --install db"}
	c := testCluster(t, f)
	err := c.Provision(context.Background(), "0123456789ab", chartBundle(t), spec)
	if err == nil || !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("err %v", err)
	}
	calls := f.joined()
	for _, want := range []string{"helm uninstall db", "helm uninstall shop", "kubectl delete namespace lab-0123456789ab", "rm -f crucible-lab-0123456789ab"} {
		if !strings.Contains(calls, want) {
			t.Errorf("cleanup missing %q", want)
		}
	}
	if _, err := os.Stat(filepath.Join(c.Dir, "0123456789ab")); !os.IsNotExist(err) {
		t.Error("lab folder left behind")
	}
}

func TestTwoClusterLabsAreIndependent(t *testing.T) {
	f := &fakeDocker{template: "kind: Deployment\n"}
	c := testCluster(t, f)
	a, b := "0123456789ab", "ba9876543210"
	for _, id := range []string{a, b} {
		if err := c.Provision(context.Background(), id, chartBundle(t), spec); err != nil {
			t.Fatal(err)
		}
	}
	pb := c.proxy(b)
	if err := c.Destroy(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if c.proxy(a) != nil || c.proxy(b) != pb || pb == nil {
		t.Fatal("destroying one lab touched the other's proxy")
	}
	if strings.Contains(f.joined(), "delete namespace lab-"+b) {
		t.Fatal("the other lab's namespace was deleted")
	}
	if pa, pb2 := readKubeconfig(t, c, a), readKubeconfig(t, c, b); pb2 == "" || pa != "" {
		t.Fatal("lab folders")
	}
}

func TestDestroyAfterRestartReopensTheProxy(t *testing.T) {
	f := &fakeDocker{template: "kind: Deployment\n"}
	c := testCluster(t, f)
	id := "0123456789ab"
	if err := c.Provision(context.Background(), id, chartBundle(t), spec); err != nil {
		t.Fatal(err)
	}
	restarted := &Cluster{Dir: c.Dir, Kubeconfig: c.Kubeconfig, Run: f.run, Bind: c.Bind} // a new process: no proxies
	f.calls = nil
	if err := restarted.Destroy(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	calls := f.joined()
	if !strings.Contains(calls, "helm uninstall shop") || !strings.Contains(calls, "kubectl delete namespace lab-0123456789ab") {
		t.Fatalf("no uninstall after restart:\n%s", calls)
	}
}

func readKubeconfig(t *testing.T, c *Cluster, id string) string {
	b, _ := os.ReadFile(filepath.Join(c.Dir, id, "kubeconfig"))
	return string(b)
}

func TestComposeLabCannotMasqueradeAsClusterLab(t *testing.T) {
	c := testCluster(t, &fakeDocker{})
	id := "0123456789ab"
	dir := filepath.Join(c.Dir, id)
	_ = os.MkdirAll(dir, 0o755)
	for _, n := range []string{".crucible-cluster", "x.cluster", id + ".cluster"} {
		_ = os.WriteFile(filepath.Join(dir, n), []byte(`{"Context":"lab","Toolbox":"evil","Charts":[{"Name":"a","Path":"p","TimeoutS":1}]}`), 0o600)
	}
	l := &Labs{Compose: Compose{Dir: c.Dir}, Cluster: c}
	if l.isCluster(id) {
		t.Fatal("a bundle's files made a compose lab a cluster lab")
	}
	if got := l.Labs(); len(got) != 1 || got[0] != id || len(c.Labs()) != 0 {
		t.Fatalf("Labs %v cluster %v", got, c.Labs())
	}
}

func TestDestroyRefusesAnInvalidSavedSpec(t *testing.T) {
	f := &fakeDocker{}
	c := testCluster(t, f)
	id := "0123456789ab"
	_ = os.MkdirAll(filepath.Join(c.Dir, id), 0o755)
	_ = os.WriteFile(markerPath(c.Dir, id), []byte(`{"Context":"lab","Toolbox":"t","Charts":[{"Name":"a;id","Path":"p","TimeoutS":1}]}`), 0o600)
	if err := c.Destroy(context.Background(), id); err == nil || f.joined() != "" {
		t.Fatalf("err %v calls %q", err, f.joined())
	}
}

func TestCRDsInChartsNeedClusterWide(t *testing.T) {
	f := &fakeDocker{template: "kind: CustomResourceDefinition\n"}
	err := testCluster(t, f).Provision(context.Background(), "0123456789ab", chartBundle(t), spec)
	if err == nil || !strings.Contains(err.Error(), "CustomResourceDefinition") || !strings.Contains(f.joined(), "helm template --include-crds") {
		t.Fatalf("err %v\n%s", err, f.joined())
	}
}

func TestUnsafeSpecsAreRefused(t *testing.T) {
	bad := []ap.ClusterSpec{}
	for _, mut := range []func(*ap.ClusterSpec){
		func(s *ap.ClusterSpec) { s.Charts = []ap.Chart{{Name: "x", Path: "charts/x;id", TimeoutS: 5}} },
		func(s *ap.ClusterSpec) {
			s.Charts = []ap.Chart{{Name: "x", Path: "charts/x", Values: "kubeconfig", TimeoutS: 5}}
		},
		func(s *ap.ClusterSpec) {
			s.Charts = []ap.Chart{{Name: "x", Path: "c", Values: ".crucible-cluster", TimeoutS: 5}}
		},
		func(s *ap.ClusterSpec) { s.Toolbox = "--privileged" },
	} {
		s := spec
		mut(&s)
		bad = append(bad, s)
	}
	for i, s := range bad {
		f := &fakeDocker{template: "kind: Deployment\n"}
		if err := testCluster(t, f).Provision(context.Background(), "0123456789ab", chartBundle(t), s); err == nil || f.joined() != "" {
			t.Errorf("%d: err %v calls %q", i, err, f.joined())
		}
	}
}

func TestEarlyFailureLeavesNothingBehind(t *testing.T) {
	f := &fakeDocker{}
	c := testCluster(t, f)
	c.Bind = func(context.Context) (string, error) { return "", errors.New("no bind") }
	id := "0123456789ab"
	if err := c.Provision(context.Background(), id, chartBundle(t), spec); err == nil {
		t.Fatal("want error")
	}
	if _, err := os.Stat(filepath.Join(c.Dir, id)); !os.IsNotExist(err) {
		t.Error("folder left")
	}
	if _, err := os.Stat(markerPath(c.Dir, id)); !os.IsNotExist(err) {
		t.Error("marker left")
	}
	if len(c.Labs()) != 0 || strings.Contains(f.joined(), "delete namespace") {
		t.Fatalf("labs %v\n%s", c.Labs(), f.joined())
	}
}

// A lab may ship a root-level kubeconfig file (0644); the toolbox kubeconfig replaces it, 0600, with only the proxy's
// token. os.WriteFile alone keeps an existing file's mode.
func TestToolboxKubeconfigIsPrivateEvenOverABundleFile(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range map[string]string{"charts/shop/Chart.yaml": "name: shop\n", "kubeconfig": "token: from-the-bundle\n"} {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(body))
	}
	_ = tw.Close()
	_ = gz.Close()
	c := testCluster(t, &fakeDocker{template: "kind: Deployment\n"})
	id := "0123456789ab"
	if err := c.Provision(context.Background(), id, buf.Bytes(), spec); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(c.Dir, id, "kubeconfig"))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("kubeconfig mode %v %v", st.Mode(), err)
	}
	if kc := readKubeconfig(t, c, id); strings.Contains(kc, "from-the-bundle") || !strings.Contains(kc, c.proxy(id).Token) {
		t.Fatalf("kubeconfig: %s", kc)
	}
}

// A lab whose context was deleted (kind delete cluster) or whose folder is gone can't be reached to uninstall: Destroy
// forgets it on this laptop instead of failing at every hello forever.
func TestDestroyForgetsALabItCanNoLongerReach(t *testing.T) {
	for name, breakIt := range map[string]func(c *Cluster, id string){
		"context deleted": func(c *Cluster, _ string) {
			if err := os.WriteFile(c.Kubeconfig, []byte("apiVersion: v1\nkind: Config\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"folder gone": func(c *Cluster, id string) { _ = os.RemoveAll(filepath.Join(c.Dir, id)) },
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeDocker{template: "kind: Deployment\n"}
			c := testCluster(t, f)
			id := "0123456789ab"
			if err := c.Provision(context.Background(), id, chartBundle(t), spec); err != nil {
				t.Fatal(err)
			}
			breakIt(c, id)
			restarted := &Cluster{Dir: c.Dir, Kubeconfig: c.Kubeconfig, Run: f.run, Bind: c.Bind}
			f.calls = nil
			if err := restarted.Destroy(context.Background(), id); err != nil {
				t.Fatal(err)
			}
			calls := f.joined()
			if strings.Contains(calls, "helm uninstall") || !strings.Contains(calls, "rm -f crucible-lab-"+id) {
				t.Fatalf("calls:\n%s", calls)
			}
			if _, err := os.Stat(filepath.Join(c.Dir, id)); !os.IsNotExist(err) {
				t.Error("folder left")
			}
			if len(restarted.Labs()) != 0 {
				t.Errorf("still reported: %v", restarted.Labs())
			}
		})
	}
}

// An unreachable cluster is different: the lab stays so a later destroy retries.
func TestDestroyKeepsALabWhoseClusterIsDown(t *testing.T) {
	f := &fakeDocker{template: "kind: Deployment\n"}
	srv, _ := upstream(t)
	c := &Cluster{Dir: t.TempDir(), Kubeconfig: kubeconfigFor(t, srv, "{token: real}"), Run: f.run,
		Bind: func(context.Context) (string, error) { return "127.0.0.1", nil }}
	id := "0123456789ab"
	if err := c.Provision(context.Background(), id, chartBundle(t), spec); err != nil {
		t.Fatal(err)
	}
	srv.Close()
	restarted := &Cluster{Dir: c.Dir, Kubeconfig: c.Kubeconfig, Run: f.run, Bind: c.Bind}
	if err := restarted.Destroy(context.Background(), id); err == nil || len(restarted.Labs()) != 1 {
		t.Fatalf("err %v labs %v", err, restarted.Labs())
	}
}
