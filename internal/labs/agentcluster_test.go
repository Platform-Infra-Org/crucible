package labs

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	ap "crucible/internal/agentproto"
	"crucible/internal/content"
)

// A cluster lab's provision carries the charts and values but never checks, setups or the lab file.
func TestClusterProvisionSendsChartsNeverChecks(t *testing.T) {
	tr, probs := content.Load("../../examples/forge-101")
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	lab := tr.Module("03-cluster-heat").Lab
	var sent ap.Msg
	a := AgentClusterRunner{Toolbox: "toolbox@sha256:abc", call: func(_ context.Context, _ int64, m ap.Msg) (ap.Msg, error) { sent = m; return ap.Msg{}, nil }}
	if err := a.ProvisionLab(context.Background(), &Instance{ID: "0123456789ab", UserID: 1, ClusterContext: "kind-dev"}, lab); err != nil {
		t.Fatal(err)
	}
	if sent.Type != ap.TProvision || sent.Cluster == nil || sent.Cluster.Context != "kind-dev" || sent.Cluster.Toolbox != "toolbox@sha256:abc" ||
		sent.Cluster.Charts[0].Name != lab.Cluster.Charts[0].Name || sent.Cluster.Charts[0].TimeoutS != int64(lab.Cluster.Charts[0].Timeout.D().Seconds()) {
		t.Fatalf("message %+v", sent.Cluster)
	}
	names := tarNames(t, sent.Data)
	if !strings.Contains(names, "charts/") {
		t.Fatalf("no charts in %s", names)
	}
	for _, never := range []string{"checks/", "setup/", "tasks/", "lab.yaml"} {
		if strings.Contains(names, never) {
			t.Fatalf("%s reached the agent: %s", never, names)
		}
	}
}

func TestClusterLabNeedsAContextTheAgentReported(t *testing.T) {
	a := AgentClusterRunner{Toolbox: "t", contexts: func(int64) []ap.KubeContext { return []ap.KubeContext{{Name: "kind-dev"}} },
		online: func(int64) bool { return true }}
	if err := a.CheckContext(1, "kind-dev"); err != nil {
		t.Fatal(err)
	}
	if err := a.CheckContext(1, "prod"); err == nil || !strings.Contains(err.Error(), `"prod" is not in your kubeconfig`) {
		t.Fatalf("unknown: %v", err)
	}
	if err := a.CheckContext(1, ""); err == nil || !strings.Contains(err.Error(), "pick a cluster") {
		t.Fatalf("none: %v", err)
	}
	if err := (AgentClusterRunner{}).Available(&Instance{UserID: 1}); err == nil || !strings.Contains(err.Error(), "CRUCIBLE_TOOLBOX_IMAGE") {
		t.Fatalf("no toolbox: %v", err)
	}
	off := AgentClusterRunner{Toolbox: "t", online: func(int64) bool { return false }}
	if err := off.Available(&Instance{UserID: 1}); !errors.Is(err, errAgentOffline) {
		t.Fatalf("offline: %v", err)
	}
}

func tarNames(t *testing.T, b []byte) string {
	gz, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var names []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return strings.Join(names, " ")
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
	}
}

func TestReadyCheckRunsCrucibleReady(t *testing.T) {
	b, err := scriptBody(&content.Lab{}, &content.Script{Ready: []string{"shop", "db"}})
	if err != nil || string(b) != "exec crucible-ready shop db\n" {
		t.Fatalf("%q %v", b, err)
	}
	if b, _ := scriptBody(&content.Lab{}, &content.Script{Ready: []string{"all"}}); string(b) != "exec crucible-ready all\n" {
		t.Fatalf("all: %q", b)
	}
	if !selfReported("cluster") || !selfReported("local") || selfReported("aws") {
		t.Fatal("cluster and laptop labs are self-reported")
	}
}
