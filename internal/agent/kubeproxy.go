package agent

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	utilnet "k8s.io/apimachinery/pkg/util/net"
	"k8s.io/apimachinery/pkg/util/proxy"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/transport"
)

// Proxy serves one context of the trainee's kubeconfig to one lab's toolbox, on a local address, behind a random
// token. It authenticates upstream with the kubeconfig itself (exec plugins, OIDC, refresh), so credentials never
// reach the container. Close it when the lab ends.
type Proxy struct {
	Addr  string
	Token string
	srv   *http.Server
}

func (p *Proxy) Close() error { return p.srv.Close() }

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
	h, err := apiHandler(cfg)
	if err != nil {
		return nil, err
	}
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	tok := hex.EncodeToString(b)
	ln, err := net.Listen("tcp", net.JoinHostPort(bind, "0"))
	if err != nil {
		return nil, err
	}
	srv := &http.Server{Handler: requireToken(tok, h), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	return &Proxy{Addr: ln.Addr().String(), Token: tok, srv: srv}, nil
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
func apiHandler(cfg *rest.Config) (http.Handler, error) {
	target, err := url.Parse(cfg.Host)
	if err != nil {
		return nil, err
	}
	if target.Scheme == "" {
		return nil, errors.New("the cluster's server address has no scheme")
	}
	rt, err := rest.TransportFor(cfg)
	if err != nil {
		return nil, err
	}
	tc, err := cfg.TransportConfig()
	if err != nil {
		return nil, err
	}
	tlsCfg, err := transport.TLSConfigFor(tc)
	if err != nil {
		return nil, err
	}
	base := utilnet.SetOldTransportDefaults(&http.Transport{TLSClientConfig: tlsCfg,
		DialContext: (&net.Dialer{Timeout: 30 * time.Second}).DialContext})
	wrap, err := transport.HTTPWrappersForConfig(tc, proxy.MirrorRequest)
	if err != nil {
		return nil, err
	}
	h := proxy.NewUpgradeAwareHandler(target, rt, false, false, responder{})
	h.UpgradeTransport = proxy.NewUpgradeRequestRoundTripper(base, wrap)
	h.UseRequestLocation = true
	h.UseLocationHost = true
	return h, nil
}
