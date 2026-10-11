package agent

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"k8s.io/client-go/tools/clientcmd"

	ap "crucible/internal/agentproto"
)

// LoadingRules finds the kubeconfig as kubectl does: the explicit file (only), else $KUBECONFIG or ~/.kube/config,
// then ~/.crucible/kubeconfig (where `crucible-agent cluster up` writes), which loses to the former on a name clash.
func LoadingRules(explicit string) *clientcmd.ClientConfigLoadingRules {
	r := clientcmd.NewDefaultClientConfigLoadingRules()
	r.ExplicitPath = explicit
	if explicit == "" {
		// clientcmd fixes ~/.kube/config when the process starts; follow $HOME as it is now.
		if os.Getenv("KUBECONFIG") == "" {
			r.Precedence = nil
			if home, err := os.UserHomeDir(); err == nil {
				r.Precedence = []string{filepath.Join(home, ".kube", "config")}
			}
		}
		if p := crucibleKubeconfig(); p != "" {
			r.Precedence = append(r.Precedence, p)
		}
	}
	return r
}

func crucibleKubeconfig() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".crucible", "kubeconfig")
}

// ReadContexts lists the kubeconfig's contexts, sorted, with their API server's host. A missing or broken file is no
// contexts, never an error: the agent keeps running laptop labs.
func ReadContexts(explicit string) []ap.KubeContext {
	cfg, err := LoadingRules(explicit).Load()
	if err != nil {
		return []ap.KubeContext{}
	}
	out := []ap.KubeContext{}
	for name, c := range cfg.Contexts {
		server := ""
		if cl := cfg.Clusters[c.Cluster]; cl != nil {
			if u, err := url.Parse(cl.Server); err == nil {
				server = u.Host
			}
		}
		out = append(out, ap.KubeContext{Name: name, Server: server})
	}
	slices.SortFunc(out, func(a, b ap.KubeContext) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// Watcher re-reads the kubeconfig every Every and sends the contexts on Changes when they differ from the last.
type Watcher struct {
	Kubeconfig string
	Every      time.Duration

	once    sync.Once
	mu      sync.Mutex
	current []ap.KubeContext
	changes chan []ap.KubeContext
}

func (w *Watcher) init() { w.once.Do(func() { w.changes = make(chan []ap.KubeContext, 1) }) }

func (w *Watcher) Changes() <-chan []ap.KubeContext { w.init(); return w.changes }

func (w *Watcher) Current() []ap.KubeContext {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.current)
}

func (w *Watcher) Run(ctx context.Context) {
	w.init()
	first := true
	for {
		got := ReadContexts(w.Kubeconfig)
		w.mu.Lock()
		changed := first || !slices.Equal(got, w.current)
		w.current = got
		w.mu.Unlock()
		if changed {
			select { // keep only the newest list
			case <-w.changes:
			default:
			}
			w.changes <- got
		}
		first = false
		select {
		case <-ctx.Done():
			return
		case <-time.After(w.Every):
		}
	}
}
