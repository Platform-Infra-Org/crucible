package agent

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	utilnet "k8s.io/apimachinery/pkg/util/net"
	"k8s.io/apimachinery/pkg/util/proxy"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/transport"
)

// Proxy serves one context of the trainee's kubeconfig to one lab's toolbox, on a local address, over TLS, behind a
// random token. It authenticates upstream with the kubeconfig itself (exec plugins, OIDC, refresh), so credentials never
// reach the container. Close it when the lab ends.
type Proxy struct {
	Addr  string
	Token string
	CA    []byte // PEM: the proxy's own self-signed certificate, for the toolbox kubeconfig's certificate-authority-data
	srv   *http.Server
	base  *http.Transport
	mu    sync.Mutex
	conns map[net.Conn]struct{}
}

// Close stops listening and drops every open connection, hijacked exec/attach/port-forward streams included, so
// nothing keeps an authenticated upstream connection after the lab ends.
func (p *Proxy) Close() error {
	err := p.srv.Close()
	p.mu.Lock()
	for c := range p.conns {
		_ = c.Close()
	}
	p.conns = nil
	p.mu.Unlock()
	p.base.CloseIdleConnections()
	return err
}

func StartProxy(ctx context.Context, kubeconfig, kubeContext, bind string) (*Proxy, error) {
	raw, err := LoadingRules(kubeconfig).Load()
	if err != nil {
		return nil, fmt.Errorf("reading your kubeconfig: %w", err)
	}
	if raw.Contexts[kubeContext] == nil {
		return nil, fmt.Errorf("%q is not in your kubeconfig any more; pick another cluster", kubeContext)
	}
	cfg, err := clientcmd.NewNonInteractiveClientConfig(*raw, kubeContext, &clientcmd.ConfigOverrides{}, nil).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("context %q: %w", kubeContext, err)
	}
	if err := probe(ctx, cfg); err != nil {
		return nil, fmt.Errorf("can't reach %s at %s: %w", kubeContext, cfg.Host, err)
	}
	h, base, err := apiHandler(cfg)
	if err != nil {
		return nil, err
	}
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	tok := hex.EncodeToString(b)
	cert, ca, err := selfSigned(bind)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(bind, "0"))
	if err != nil {
		return nil, err
	}
	// TLS because kubectl and helm (clientcmd) send a kubeconfig's token only over https. HTTP/1.1 only: exec, attach
	// and port-forward upgrade the connection.
	ln = tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"http/1.1"}, MinVersion: tls.VersionTLS12})
	p := &Proxy{Addr: ln.Addr().String(), Token: tok, CA: ca, base: base, conns: map[net.Conn]struct{}{}}
	p.srv = &http.Server{Handler: requireToken(tok, h), ReadHeaderTimeout: 10 * time.Second,
		ConnState: func(c net.Conn, st http.ConnState) {
			p.mu.Lock()
			defer p.mu.Unlock()
			if p.conns == nil { // already closed
				_ = c.Close()
				return
			}
			switch st {
			case http.StateActive, http.StateHijacked:
				p.conns[c] = struct{}{}
			case http.StateIdle, http.StateClosed:
				delete(p.conns, c)
			}
		}}
	go func() { _ = p.srv.Serve(ln) }()
	return p, nil
}

// selfSigned makes the proxy's certificate: for host.docker.internal (the toolbox's name for this machine) and the
// address it listens on, with a random serial. Valid for a fixed 7 days; each new proxy (a provision, or a destroy
// after an agent restart) issues a new one.
func selfSigned(bind string) (tls.Certificate, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	tmpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "crucible lab proxy"},
		DNSNames: []string{"host.docker.internal"}, IPAddresses: []net.IP{net.ParseIP(bind)},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(7 * 24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

// probe asks the API server for its version, so a cluster that is down or a credential plugin that fails is reported
// at provisioning, naming the context.
func probe(ctx context.Context, cfg *rest.Config) error {
	c := rest.CopyConfig(cfg)
	c.Timeout = 15 * time.Second
	dc, err := discovery.NewDiscoveryClientForConfig(c)
	if err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { _, err := dc.ServerVersion(); done <- err }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func requireToken(tok string, next http.Handler) http.Handler {
	want := []byte("Bearer " + tok)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		r.Header.Del("Authorization") // the upstream transport adds the real credentials
		next.ServeHTTP(w, r)
	})
}

type responder struct{}

func (responder) Error(w http.ResponseWriter, _ *http.Request, err error) {
	http.Error(w, err.Error(), http.StatusBadGateway)
}

// apiHandler proxies every path to the API server, upgrades (exec, attach, port-forward) and watches included, as
// kubectl proxy does.
func apiHandler(cfg *rest.Config) (http.Handler, *http.Transport, error) {
	target, err := url.Parse(cfg.Host)
	if err != nil {
		return nil, nil, err
	}
	if target.Scheme == "" {
		return nil, nil, errors.New("the cluster's server address has no scheme")
	}
	if target.Path == "" { // with no path, the handler 301s every GET to path+"/" (there is no /openapi/v3/api/v1/)
		target.Path = "/"
	}
	rt, err := rest.TransportFor(cfg)
	if err != nil {
		return nil, nil, err
	}
	tc, err := cfg.TransportConfig()
	if err != nil {
		return nil, nil, err
	}
	tlsCfg, err := transport.TLSConfigFor(tc)
	if err != nil {
		return nil, nil, err
	}
	base := utilnet.SetOldTransportDefaults(&http.Transport{TLSClientConfig: tlsCfg,
		DialContext: (&net.Dialer{Timeout: 30 * time.Second}).DialContext})
	wrap, err := transport.HTTPWrappersForConfig(tc, proxy.MirrorRequest)
	if err != nil {
		return nil, nil, err
	}
	h := proxy.NewUpgradeAwareHandler(target, rt, false, false, responder{})
	h.UpgradeTransport = proxy.NewUpgradeRequestRoundTripper(base, wrap)
	h.UseRequestLocation = true
	h.UseLocationHost = true
	h.AppendLocationPath = true // a server URL with a path (Rancher) keeps it
	return h, base, nil
}
