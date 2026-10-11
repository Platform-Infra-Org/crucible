package agent

import (
	"context"
	"os"
	"path/filepath"
	"time"

	ap "crucible/internal/agentproto"
)

// Labs runs every lab on this laptop: compose labs (runtime local) and cluster labs, told apart by the cluster
// marker in the lab's folder. Both share one labs folder.
type Labs struct {
	Compose Compose
	Cluster *Cluster // nil: no kubeconfig support
}

func (l *Labs) isCluster(id string) bool {
	_, err := os.Stat(filepath.Join(l.Compose.Dir, id, clusterMarker))
	return l.Cluster != nil && err == nil
}

func (l *Labs) Provision(ctx context.Context, id string, bundle []byte, compose string) error {
	return l.Compose.Provision(ctx, id, bundle, compose)
}

func (l *Labs) ProvisionCluster(ctx context.Context, id string, bundle []byte, s ap.ClusterSpec) error {
	if l.Cluster == nil {
		return errNoKube
	}
	return l.Cluster.Provision(ctx, id, bundle, s)
}

func (l *Labs) Destroy(ctx context.Context, id string) error {
	if l.isCluster(id) {
		return l.Cluster.Destroy(ctx, id)
	}
	return l.Compose.Destroy(ctx, id)
}

func (l *Labs) RunScript(ctx context.Context, id, service string, script []byte, env map[string]string, timeout time.Duration) (ap.Msg, error) {
	if l.isCluster(id) {
		return l.Cluster.RunScript(ctx, id, script, env, timeout)
	}
	return l.Compose.RunScript(ctx, id, service, script, env, timeout)
}

func (l *Labs) StartPTY(id, service string, cols, rows int) (Session, error) {
	if l.isCluster(id) {
		return l.Cluster.StartPTY(id, cols, rows)
	}
	return l.Compose.StartPTY(id, service, cols, rows)
}

func (l *Labs) DestroyAll(ctx context.Context) {
	for _, id := range l.Labs() {
		_ = l.Destroy(ctx, id)
	}
}

func (l *Labs) Labs() []string {
	ids := l.Compose.Labs()
	if l.Cluster != nil {
		ids = append(ids, l.Cluster.Labs()...)
	}
	return ids
}
