//go:build cluster

package labs

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
	utilexec "k8s.io/client-go/util/exec"
)

// TestClusterLabOnKind runs Forge 101's cluster lab on a real cluster as the crucible service account
// (scripts/cluster-check.sh sets the environment). Never runs in `go test ./...` (build tag cluster).
func TestClusterLabOnKind(t *testing.T) {
	kc := os.Getenv("CRUCIBLE_TEST_KUBECONFIG")
	if kc == "" {
		t.Skip("run through make cluster-check")
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", kc)
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewClusterRunner(cfg, os.Getenv("CRUCIBLE_CLUSTER_PRIVILEGED") == "1")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	dir := "../../examples/forge-101/modules/03-cluster-heat/lab"
	bundle, err := Bundle(dir)
	if err != nil {
		t.Fatal(err)
	}
	inst := &Instance{ID: newLabID(), Runtime: "cluster"}
	t.Cleanup(func() { _ = r.Destroy(context.Background(), inst) })
	if err := r.Provision(ctx, inst, bundle, "compose.yaml"); err != nil {
		t.Fatalf("provision: %v", err)
	}
	script := func(svc, body string, env map[string]string, timeout time.Duration) ScriptResult {
		t.Helper()
		res, err := r.RunScript(ctx, inst, ScriptSpec{Service: svc, Script: []byte(body), Env: env, Timeout: timeout})
		if err != nil {
			t.Fatalf("run %q: %v", body, err)
		}
		return res
	}
	check, _ := os.ReadFile(filepath.Join(dir, "checks/01-cast.sh"))

	t.Run("check fails before the trainee acts", func(t *testing.T) {
		if res := script("shell", string(check), nil, 30*time.Second); res.ExitCode == 0 {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("terminal over exec, then the server-side check passes", func(t *testing.T) {
		p, err := r.OpenPTY(ctx, inst, "shell", 100, 30)
		if err != nil {
			t.Fatal(err)
		}
		var mu sync.Mutex
		var seen bytes.Buffer
		go func() {
			buf := make([]byte, 4096)
			for {
				n, err := p.Read(buf)
				mu.Lock()
				seen.Write(buf[:n])
				mu.Unlock()
				if err != nil {
					return
				}
			}
		}()
		_, _ = p.Write([]byte("echo 'hello crucible' > /tmp/cast.txt; echo DONE-$((40+2))\n"))
		deadline := time.Now().Add(30 * time.Second)
		for {
			mu.Lock()
			ok := strings.Contains(seen.String(), "DONE-42") // the echoed command line shows $((40+2)), not 42
			mu.Unlock()
			if ok {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("no shell output: %q", seen.String())
			}
			time.Sleep(200 * time.Millisecond)
		}
		_ = p.Close()
		if res := script("shell", string(check), nil, 30*time.Second); res.ExitCode != 0 || !strings.Contains(res.Output, "Cast in the crucible.") {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("answers are data, never shell", func(t *testing.T) {
		evil := `$(touch /tmp/pwned); ' " ; rm -rf /`
		res := script("shell", `printf '%s' "$CRUCIBLE_ANSWER"; test ! -e /tmp/pwned`, map[string]string{"CRUCIBLE_ANSWER": evil}, 30*time.Second)
		if res.ExitCode != 0 || res.Output != evil {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("a quick check ends well before its timeout (stdin EOF reaches sh -s)", func(t *testing.T) {
		start := time.Now()
		res := script("shell", "echo quick", nil, 60*time.Second)
		if res.ExitCode != 0 || res.TimedOut || time.Since(start) > 20*time.Second || !strings.Contains(res.Output, "quick") {
			t.Fatalf("%+v after %v", res, time.Since(start))
		}
	})
	t.Run("a hanging check times out", func(t *testing.T) {
		start := time.Now()
		res := script("shell", "sleep 120", nil, 2*time.Second)
		if !res.TimedOut || time.Since(start) > 20*time.Second {
			t.Fatalf("%+v after %v", res, time.Since(start))
		}
	})
	t.Run("busybox timeout -s KILL works in the dind image", func(t *testing.T) {
		start := time.Now()
		var out bytes.Buffer
		err := r.run(ctx, labNamespace(inst.ID), []string{"timeout", "-s", "KILL", "2", "sleep", "60"}, nil, &out)
		var exit utilexec.ExitError
		if !errors.As(err, &exit) || exit.ExitStatus() != 137 || time.Since(start) > 20*time.Second {
			t.Fatalf("want exit 137 quickly, got %v after %v: %s", err, time.Since(start), out.String())
		}
	})
	t.Run("egress", func(t *testing.T) {
		probe := os.Getenv("CRUCIBLE_TEST_PROBE_IP")
		blocked := map[string]string{
			"neighbour pod":    probe + " 80",
			"IMDS":             "169.254.169.254 80",
			"kube API cluster": os.Getenv("CRUCIBLE_TEST_API_IP") + " 443",
			"node kubelet":     os.Getenv("CRUCIBLE_TEST_NODE_IP") + " 10250",
		}
		reach := func(hostport string) string { return "echo | nc -w 4 " + hostport }
		// from a compose container inside the lab (a trainee's own workload) and from the lab pod itself
		if res := script("shell", reach("1.1.1.1 443"), nil, 30*time.Second); res.ExitCode != 0 {
			t.Fatalf("a public host must be reachable (control for the probes below): %+v", res)
		}
		var out bytes.Buffer
		if err := r.run(ctx, labNamespace(inst.ID), []string{"sh", "-c", reach("1.1.1.1 443")}, nil, &out); err != nil {
			t.Fatalf("public host from the lab pod: %v %s", err, out.String())
		}
		for what, hp := range blocked {
			if strings.HasPrefix(hp, " ") {
				t.Fatalf("missing CRUCIBLE_TEST_* address for %s", what)
			}
			if res := script("shell", reach(hp), nil, 30*time.Second); res.ExitCode == 0 {
				t.Errorf("%s (%s) must be unreachable from a compose container in a lab: %+v", what, hp, res)
			}
			out.Reset()
			if err := r.run(ctx, labNamespace(inst.ID), []string{"sh", "-c", reach(hp)}, nil, &out); err == nil {
				t.Errorf("%s (%s) must be unreachable from the lab pod", what, hp)
			}
		}
		for _, name := range []string{"registry-1.docker.io", "kubernetes.default.svc.cluster.local"} {
			if res := script("shell", "nslookup "+name, nil, 30*time.Second); res.ExitCode != 0 {
				t.Errorf("DNS must work for %s (image pulls need it): %+v", name, res)
			}
		}
	})
	t.Run("dockerd is not on the network", func(t *testing.T) {
		var out bytes.Buffer
		if err := r.run(ctx, labNamespace(inst.ID), []string{"netstat", "-ltn"}, nil, &out); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String(), ":2375") || strings.Contains(out.String(), ":2376") {
			t.Fatalf("docker API listening on TCP:\n%s", out.String())
		}
	})
	t.Run("the service account is confined to lab namespaces by the admission policy", func(t *testing.T) {
		const policy = "crucible-api may only" // the admission message; a plain RBAC or not-found error would not match
		denied := func(what string, err error) {
			t.Helper()
			if err == nil || !strings.Contains(err.Error(), policy) {
				t.Errorf("%s must be refused by the admission policy, got: %v", what, err)
			}
		}
		core := r.Client.CoreV1()
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "escape"}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "x", Image: "busybox"}}}}
		_, err := core.Pods("default").Create(ctx, pod, metav1.CreateOptions{})
		if err == nil {
			_ = core.Pods("default").Delete(ctx, "escape", metav1.DeleteOptions{})
		}
		denied("a pod in default", err)
		_, err = core.Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "lab-nolabel000"}}, metav1.CreateOptions{})
		denied("a lab- namespace without the lab label", err)
		_, err = core.Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "evil", Labels: map[string]string{labLabel: "x"}}}, metav1.CreateOptions{})
		denied("a labelled namespace not named lab-*", err)
		denied("deleting a non-lab namespace", core.Namespaces().Delete(ctx, "crucible-probe", metav1.DeleteOptions{}))
		var out bytes.Buffer
		err = r.realExec(ctx, "kube-system", os.Getenv("CRUCIBLE_TEST_DNS_POD"), []string{"true"}, remotecommand.StreamOptions{Stdout: &out, Stderr: &out})
		denied("exec into a kube-system pod", err)
		// ponytail: the privileged-PSA rule is not rendered on kind (dev); helm unit tests cover it.
	})
	t.Run("destroy", func(t *testing.T) {
		if err := r.Destroy(ctx, inst); err != nil {
			t.Fatal(err)
		}
		if ns, err := r.Client.CoreV1().Namespaces().Get(ctx, labNamespace(inst.ID), metav1.GetOptions{}); err == nil && ns.DeletionTimestamp == nil {
			t.Fatal("namespace not being deleted")
		}
		if err := r.Destroy(ctx, inst); err != nil { // usually still terminating here: must be success
			t.Fatalf("destroy of a terminating namespace: %v", err)
		}
		if ids, err := r.Live(ctx); err != nil || slices.Contains(ids, inst.ID) {
			t.Fatalf("destroyed lab still live: %v %v", ids, err)
		}
		if err := r.Destroy(ctx, inst); err != nil {
			t.Fatalf("second destroy: %v", err)
		}
	})
}

// TestClusterPSAPolicy: with the production policy rendered, a lab namespace without baseline Pod Security is refused.
func TestClusterPSAPolicy(t *testing.T) {
	kc := os.Getenv("CRUCIBLE_TEST_KUBECONFIG")
	if kc == "" || os.Getenv("CRUCIBLE_TEST_STRICT") != "1" {
		t.Skip("run through make cluster-check")
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", kc)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "lab-" + newLabID(), Labels: map[string]string{labLabel: "x"}}}
	var err2 error
	for i := 0; i < 30; i++ { // the new policy takes a few seconds to be enforced
		if _, err2 = cs.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{}); err2 != nil && strings.Contains(err2.Error(), "baseline") {
			return
		}
		_ = cs.CoreV1().Namespaces().Delete(ctx, ns.Name, metav1.DeleteOptions{})
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("a lab namespace without baseline PSA must be refused by admission, got: %v", err2)
}
