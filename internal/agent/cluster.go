package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"

	ap "crucible/internal/agentproto"
)

// clusterMarker holds a cluster lab's spec (JSON) in its folder; its presence is what makes the folder a cluster lab.
const clusterMarker = ".crucible-cluster"

var chartName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,51}[a-z0-9])?$`)
var kindLine = regexp.MustCompile(`(?m)^kind:\s*["']?([A-Za-z]+)`)

// Cluster runs cluster labs: a lab's Helm charts on a context of the trainee's kubeconfig, through a per-lab proxy
// and a toolbox container named crucible-lab-<id>. Lab folders live under Dir/<id>, beside compose labs.
type Cluster struct {
	Dir        string
	Kubeconfig string                                                    // explicit kubeconfig; "" = $KUBECONFIG or ~/.kube/config
	Bind       func(ctx context.Context) (string, error)                 // where proxies listen (BindAddr)
	Run        func(ctx context.Context, args ...string) ([]byte, error) // docker; nil = the real one

	mu      sync.Mutex
	proxies map[string]*Proxy
}

func (c *Cluster) run(ctx context.Context, args ...string) ([]byte, error) {
	if c.Run != nil {
		return c.Run(ctx, args...)
	}
	return docker(ctx, args...).CombinedOutput()
}

func (c *Cluster) proxy(id string) *Proxy {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.proxies[id]
}

func container(id string) string { return "crucible-lab-" + id }
func namespace(id string) string { return "lab-" + id }

func releases(s ap.ClusterSpec) string {
	names := make([]string, len(s.Charts))
	for i, ch := range s.Charts {
		names[i] = ch.Name
	}
	return strings.Join(names, " ")
}

func checkSpec(s ap.ClusterSpec) error {
	if s.Context == "" || s.Toolbox == "" || len(s.Charts) == 0 {
		return errors.New("invalid cluster lab: context, toolbox and charts are required")
	}
	for _, ch := range s.Charts {
		if !chartName.MatchString(ch.Name) || !filepath.IsLocal(filepath.FromSlash(ch.Path)) ||
			(ch.Values != "" && !filepath.IsLocal(filepath.FromSlash(ch.Values))) || ch.TimeoutS <= 0 {
			return fmt.Errorf("invalid chart %q", ch.Name)
		}
	}
	return nil
}

// openProxy starts the lab's proxy and writes the toolbox's kubeconfig: the proxy's address, its token, the lab's
// namespace as the default. Mode 0600: the toolbox runs as this user.
func (c *Cluster) openProxy(ctx context.Context, id string, s ap.ClusterSpec) error {
	bind, err := c.Bind(ctx)
	if err != nil {
		return err
	}
	p, err := StartProxy(ctx, c.Kubeconfig, s.Context, bind)
	if err != nil {
		return err
	}
	_, port, _ := net.SplitHostPort(p.Addr)
	kc := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters: [{name: lab, cluster: {server: "http://host.docker.internal:%s"}}]
users: [{name: lab, user: {token: %q}}]
contexts: [{name: %q, context: {cluster: lab, user: lab, namespace: %s}}]
current-context: %q
`, port, p.Token, s.Context, namespace(id), s.Context)
	if err := os.WriteFile(filepath.Join(c.Dir, id, "kubeconfig"), []byte(kc), 0o600); err != nil {
		_ = p.Close()
		return err
	}
	c.mu.Lock()
	if c.proxies == nil {
		c.proxies = map[string]*Proxy{}
	}
	c.proxies[id] = p
	c.mu.Unlock()
	return nil
}

func (c *Cluster) startToolbox(ctx context.Context, id string, s ap.ClusterSpec) error {
	_, _ = c.run(ctx, "rm", "-f", container(id))
	args := []string{"run", "-d", "--name", container(id), "--label", "crucible.lab=" + id,
		"--add-host", "host.docker.internal:host-gateway", "-v", filepath.Join(c.Dir, id) + ":/lab:ro",
		"-e", "KUBECONFIG=/lab/kubeconfig", "-e", "LAB_NAMESPACE=" + namespace(id), "-e", "LAB_ID=" + id,
		"-e", "LAB_RELEASES=" + releases(s), "-e", "CRUCIBLE_CONTEXT=" + s.Context, "-e", "HOME=/tmp"}
	if u := userFlag(); u != "" {
		args = append(args, "--user", u)
	}
	if out, err := c.run(ctx, append(args, s.Toolbox)...); err != nil {
		return fmt.Errorf("starting the lab toolbox: %w: %s", err, tail(out))
	}
	return nil
}

// userFlag runs the toolbox as this user where Docker maps uids (Linux), so it can read the 0600 kubeconfig.
func userFlag() string {
	if uid, gid := os.Getuid(), os.Getgid(); uid > 0 {
		return fmt.Sprintf("%d:%d", uid, gid)
	}
	return ""
}

func (c *Cluster) inToolbox(ctx context.Context, id, script string) ([]byte, error) {
	return c.run(ctx, "exec", container(id), "sh", "-c", script)
}

func (c *Cluster) Provision(ctx context.Context, id string, bundle []byte, s ap.ClusterSpec) (err error) {
	if err := validID(id); err != nil {
		return err
	}
	if err := checkSpec(s); err != nil {
		return err
	}
	dir := filepath.Join(c.Dir, id)
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := Untar(bundle, dir); err != nil {
		return err
	}
	spec, _ := json.Marshal(s)
	if err := os.WriteFile(filepath.Join(dir, clusterMarker), spec, 0o600); err != nil {
		return err
	}
	defer func() {
		if err != nil { // leave nothing behind: what was installed is uninstalled, the namespace goes
			cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
			defer cancel()
			_ = c.Destroy(cctx, id)
		}
	}()
	if err := c.openProxy(ctx, id, s); err != nil {
		return err
	}
	if err := c.startToolbox(ctx, id, s); err != nil {
		return err
	}
	ns := namespace(id)
	if out, err := c.inToolbox(ctx, id, "kubectl create namespace "+ns+" && kubectl label namespace "+ns+
		" app.kubernetes.io/managed-by=crucible crucible.lab="+id); err != nil {
		return fmt.Errorf("creating namespace %s: %w: %s", ns, err, tail(out))
	}
	for _, ch := range s.Charts {
		values := ""
		if ch.Values != "" {
			values = " -f /lab/" + ch.Values
		}
		work := "/tmp/charts/" + ch.Name // /lab is read-only; helm dependency build writes into the chart
		out, err := c.inToolbox(ctx, id, "rm -rf "+work+" && mkdir -p /tmp/charts && cp -r /lab/"+ch.Path+" "+work+
			" && helm dependency build "+work+" >/dev/null && helm template "+ch.Name+" "+work+" -n "+ns+values)
		if err != nil {
			return fmt.Errorf("rendering chart %s: %w: %s", ch.Name, err, tail(out))
		}
		if !s.ClusterWide {
			for _, m := range kindLine.FindAllStringSubmatch(string(out), -1) {
				if ap.ClusterScopedKinds[m[1]] {
					return fmt.Errorf("chart %s creates a %s, which is cluster-wide, but this lab doesn't declare cluster_wide; tell the training's editors", ch.Name, m[1])
				}
			}
		}
		if out, err := c.inToolbox(ctx, id, fmt.Sprintf("helm upgrade --install %s %s -n %s%s --wait --timeout %ds",
			ch.Name, work, ns, values, ch.TimeoutS)); err != nil {
			return fmt.Errorf("installing chart %s: %w: %s", ch.Name, err, tail(out))
		}
	}
	return nil
}

// Destroy uninstalls the charts in reverse order, deletes the namespace, then the toolbox, the proxy and the folder.
// After an agent restart there is no proxy or toolbox yet: both come back from the saved spec first.
func (c *Cluster) Destroy(ctx context.Context, id string) error {
	if err := validID(id); err != nil {
		return err
	}
	dir := filepath.Join(c.Dir, id)
	raw, err := os.ReadFile(filepath.Join(dir, clusterMarker))
	if errors.Is(err, fs.ErrNotExist) {
		return os.RemoveAll(dir)
	}
	var s ap.ClusterSpec
	if err != nil || json.Unmarshal(raw, &s) != nil {
		return fmt.Errorf("lab %s: unreadable cluster spec", id)
	}
	if c.proxy(id) == nil {
		if err := c.openProxy(ctx, id, s); err != nil {
			return err // keep the folder: a later destroy retries once the cluster is reachable
		}
	}
	if out, _ := c.run(ctx, "inspect", "-f", "{{.State.Running}}", container(id)); strings.TrimSpace(string(out)) != "true" {
		if err := c.startToolbox(ctx, id, s); err != nil {
			return err
		}
	}
	var script strings.Builder
	for _, ch := range slices.Backward(s.Charts) {
		fmt.Fprintf(&script, "helm uninstall %s -n %s --wait --ignore-not-found; ", ch.Name, namespace(id))
	}
	fmt.Fprintf(&script, "kubectl delete namespace %s --ignore-not-found --wait=true --timeout=5m", namespace(id))
	if out, err := c.inToolbox(ctx, id, script.String()); err != nil {
		return fmt.Errorf("removing lab %s from %s: %w: %s", id, s.Context, err, tail(out))
	}
	_, _ = c.run(ctx, "rm", "-f", container(id))
	c.mu.Lock()
	if p := c.proxies[id]; p != nil {
		_ = p.Close()
		delete(c.proxies, id)
	}
	c.mu.Unlock()
	return os.RemoveAll(dir)
}

func (c *Cluster) Labs() []string {
	entries, _ := os.ReadDir(c.Dir)
	ids := []string{}
	for _, e := range entries {
		if _, err := os.Stat(filepath.Join(c.Dir, e.Name(), clusterMarker)); e.IsDir() && validID(e.Name()) == nil && err == nil {
			ids = append(ids, e.Name())
		}
	}
	return ids
}

func (c *Cluster) RunScript(ctx context.Context, id string, script []byte, env map[string]string, timeout time.Duration) (ap.Msg, error) {
	if err := validID(id); err != nil {
		return ap.Msg{}, err
	}
	args := []string{"exec", "-i"}
	for k, v := range env {
		args = append(args, "-e", k+"="+v)
	}
	cmd := docker(context.Background(), append(args, container(id), "bash", "-s")...)
	cmd.Stdin = bytes.NewReader(script)
	return RunLimited(ctx, cmd, timeout)
}

func (c *Cluster) StartPTY(id string, cols, rows int) (Session, error) {
	if err := validID(id); err != nil {
		return nil, err
	}
	cmd := docker(context.Background(), "exec", "-it", container(id), "bash", "-l")
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		return nil, err
	}
	return &ptySession{f: f, cmd: cmd}, nil
}
