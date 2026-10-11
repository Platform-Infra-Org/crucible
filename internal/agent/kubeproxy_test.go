package agent

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// upstream is a fake API server: it records the Authorization it sees, streams a watch, and upgrades /exec.
func upstream(t *testing.T) (*httptest.Server, *string) {
	seen := new(string)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*seen = r.Header.Get("Authorization")
		switch {
		case r.URL.Path == "/version":
			fmt.Fprint(w, `{"major":"1","minor":"34"}`)
		case r.URL.Query().Get("watch") == "true":
			f := w.(http.Flusher)
			for i := 0; i < 2; i++ {
				fmt.Fprintf(w, `{"type":"ADDED","n":%d}`+"\n", i)
				f.Flush()
			}
		case r.URL.Path == "/exec":
			conn, buf, _ := w.(http.Hijacker).Hijack()
			defer conn.Close()
			buf.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: SPDY/3.1\r\n\r\n")
			buf.Flush()
			line, _ := buf.ReadString('\n')
			buf.WriteString("echo:" + line)
			buf.Flush()
		default:
			fmt.Fprint(w, `{"kind":"PodList"}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, seen
}

func kubeconfigFor(t *testing.T, srv *httptest.Server, user string) string {
	ca := base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
	p := filepath.Join(t.TempDir(), "config")
	body := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters: [{name: c, cluster: {server: %q, certificate-authority-data: %s}}]
contexts: [{name: lab, context: {cluster: c, user: u}}]
users: [{name: u, user: %s}]
`, srv.URL, ca, user)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestProxyAddsTheRealCredentialsAndNeedsItsToken(t *testing.T) {
	srv, seen := upstream(t)
	p, err := StartProxy(context.Background(), kubeconfigFor(t, srv, "{token: real-upstream-token}"), "lab", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	get := func(path, token string) (*http.Response, string) {
		req, _ := http.NewRequest("GET", "http://"+p.Addr+path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		return res, string(b)
	}
	if res, _ := get("/api/v1/namespaces/x/pods", ""); res.StatusCode != 401 {
		t.Fatalf("no token: %d", res.StatusCode)
	}
	if res, _ := get("/api/v1/namespaces/x/pods", "wrong"); res.StatusCode != 401 {
		t.Fatalf("wrong token: %d", res.StatusCode)
	}
	if res, body := get("/api/v1/namespaces/x/pods", p.Token); res.StatusCode != 200 || !strings.Contains(body, "PodList") || *seen != "Bearer real-upstream-token" {
		t.Fatalf("proxied: %d %s, upstream saw %q", res.StatusCode, body, *seen)
	}
	if _, body := get("/api/v1/pods?watch=true", p.Token); strings.Count(body, "ADDED") != 2 {
		t.Fatalf("watch: %q", body)
	}
	if !strings.HasPrefix(p.Addr, "127.0.0.1:") {
		t.Fatalf("listens on %s", p.Addr)
	}
}

func TestProxyPassesUpgradedStreams(t *testing.T) {
	srv, _ := upstream(t)
	p, err := StartProxy(context.Background(), kubeconfigFor(t, srv, "{token: t}"), "lab", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	c, err := net.Dial("tcp", p.Addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	fmt.Fprintf(c, "POST /exec HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer %s\r\nConnection: Upgrade\r\nUpgrade: SPDY/3.1\r\n\r\n", p.Token)
	r := bufio.NewReader(c)
	status, _ := r.ReadString('\n')
	if !strings.Contains(status, "101") {
		t.Fatalf("status %q", status)
	}
	for line, _ := r.ReadString('\n'); line != "\r\n"; line, _ = r.ReadString('\n') {
	}
	fmt.Fprint(c, "hello\n")
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if got, _ := r.ReadString('\n'); got != "echo:hello\n" {
		t.Fatalf("stream: %q", got)
	}
}

func TestProxyProbeNamesTheContextWhenCredentialsFail(t *testing.T) {
	srv, _ := upstream(t)
	cfg := kubeconfigFor(t, srv, "{exec: {apiVersion: client.authentication.k8s.io/v1, command: /nonexistent/aws, args: [eks, get-token], interactiveMode: Never}}")
	_, err := StartProxy(context.Background(), cfg, "lab", "127.0.0.1")
	if err == nil || !strings.Contains(err.Error(), `can't reach lab`) || !strings.Contains(err.Error(), "/nonexistent/aws") {
		t.Fatalf("err = %v", err)
	}
}

func TestProxyClosesAndUnknownContextFails(t *testing.T) {
	srv, _ := upstream(t)
	cfg := kubeconfigFor(t, srv, "{token: t}")
	if _, err := StartProxy(context.Background(), cfg, "nope", "127.0.0.1"); err == nil || !strings.Contains(err.Error(), `"nope" is not in your kubeconfig`) {
		t.Fatalf("unknown context: %v", err)
	}
	p, err := StartProxy(context.Background(), cfg, "lab", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	_ = p.Close()
	if _, err := net.DialTimeout("tcp", p.Addr, time.Second); err == nil {
		t.Fatal("still listening after Close")
	}
}
