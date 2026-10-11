package agent

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
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

// clientFor and dial trust the proxy's own certificate, as the toolbox kubeconfig does.
func tlsFor(p *Proxy) *tls.Config {
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(p.CA)
	return &tls.Config{RootCAs: pool}
}

func clientFor(p *Proxy) *http.Client {
	return &http.Client{Transport: &http.Transport{TLSClientConfig: tlsFor(p)}}
}

func dial(p *Proxy) (net.Conn, error) { return tls.Dial("tcp", p.Addr, tlsFor(p)) }

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
		req, _ := http.NewRequest("GET", "https://"+p.Addr+path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := clientFor(p).Do(req)
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
	srv, seen := upstream(t)
	p, err := StartProxy(context.Background(), kubeconfigFor(t, srv, "{token: t}"), "lab", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	nc, err := dial(p)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	fmt.Fprint(nc, "POST /exec HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\nUpgrade: SPDY/3.1\r\n\r\n")
	if st, _ := bufio.NewReader(nc).ReadString('\n'); !strings.Contains(st, "401") {
		t.Fatalf("tokenless upgrade: %q", st)
	}
	c, err := dial(p)
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
	if *seen != "Bearer t" {
		t.Fatalf("upstream saw %q on the upgrade", *seen)
	}
}

func TestProxyHonoursAServerPathPrefix(t *testing.T) {
	var paths []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if !strings.HasPrefix(r.URL.Path, "/prefix/") {
			http.NotFound(w, r)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/exec") {
			conn, buf, _ := w.(http.Hijacker).Hijack()
			defer conn.Close()
			buf.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: SPDY/3.1\r\n\r\n")
			buf.Flush()
			return
		}
		fmt.Fprint(w, `{"kind":"PodList","major":"1"}`)
	}))
	defer srv.Close()
	cfg := kubeconfigFor(t, srv, "{token: t}")
	b, _ := os.ReadFile(cfg)
	_ = os.WriteFile(cfg, []byte(strings.Replace(string(b), srv.URL, srv.URL+"/prefix", 1)), 0o600)
	p, err := StartProxy(context.Background(), cfg, "lab", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	req, _ := http.NewRequest("GET", "https://"+p.Addr+"/api/v1/pods", nil)
	req.Header.Set("Authorization", "Bearer "+p.Token)
	res, err := clientFor(p).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("plain: %d, upstream saw %v", res.StatusCode, paths)
	}
	c, err := dial(p)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	fmt.Fprintf(c, "POST /api/v1/exec HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer %s\r\nConnection: Upgrade\r\nUpgrade: SPDY/3.1\r\n\r\n", p.Token)
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if st, _ := bufio.NewReader(c).ReadString('\n'); !strings.Contains(st, "101") {
		t.Fatalf("upgrade: %q, upstream saw %v", st, paths)
	}
}

func TestProxyCloseEndsOpenStreams(t *testing.T) {
	srv, _ := upstream(t)
	p, err := StartProxy(context.Background(), kubeconfigFor(t, srv, "{token: t}"), "lab", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	c, err := dial(p)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	fmt.Fprintf(c, "POST /exec HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer %s\r\nConnection: Upgrade\r\nUpgrade: SPDY/3.1\r\n\r\n", p.Token)
	r := bufio.NewReader(c)
	for line, _ := r.ReadString('\n'); line != "\r\n"; line, _ = r.ReadString('\n') {
	}
	_ = p.Close()
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := r.ReadByte(); err == nil || strings.Contains(err.Error(), "timeout") {
		t.Fatalf("stream survived Close: %v", err)
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

// A kubeconfig that authenticates with a client certificate (kind's admin.kubeconfig): the proxy presents it upstream on
// plain requests and on upgrades alike.
func TestProxyPresentsAClientCertificateUpstream(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "kubernetes-admin"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	leaf, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	keyDER, _ := x509.MarshalECPrivateKey(key)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/exec" {
			conn, buf, _ := w.(http.Hijacker).Hijack()
			defer conn.Close()
			buf.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: SPDY/3.1\r\n\r\n")
			buf.Flush()
			return
		}
		fmt.Fprint(w, `{"major":"1","minor":"34"}`)
	}))
	srv.TLS = &tls.Config{ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool}
	srv.StartTLS()
	defer srv.Close()
	b64 := func(b []byte) string { return base64.StdEncoding.EncodeToString(b) }
	p, err := StartProxy(context.Background(), kubeconfigFor(t, srv, fmt.Sprintf("{client-certificate-data: %s, client-key-data: %s}", b64(certPEM), b64(keyPEM))), "lab", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	req, _ := http.NewRequest("GET", "https://"+p.Addr+"/api/v1/pods", nil)
	req.Header.Set("Authorization", "Bearer "+p.Token)
	res, err := clientFor(p).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("plain: %d", res.StatusCode)
	}
	c, err := dial(p)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	fmt.Fprintf(c, "POST /exec HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer %s\r\nConnection: Upgrade\r\nUpgrade: SPDY/3.1\r\n\r\n", p.Token)
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if st, _ := bufio.NewReader(c).ReadString('\n'); !strings.Contains(st, "101") {
		t.Fatalf("upgrade: %q", st)
	}
}
