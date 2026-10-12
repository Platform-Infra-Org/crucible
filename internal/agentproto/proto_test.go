package agentproto

import (
	"encoding/json"
	"strings"
	"testing"
)

// A cluster provision carries its spec; a laptop lab's messages look exactly as before, so old agents still work.
func TestClusterFieldsTravelAndStayOutOfOtherMessages(t *testing.T) {
	m := Msg{Type: TProvision, LabID: "0123456789ab", Cluster: &ClusterSpec{Context: "kind-dev", Toolbox: "img@sha256:x",
		Charts: []Chart{{Name: "shop", Path: "charts/shop", Values: "values/shop.yaml", TimeoutS: 300}}, ClusterWide: true}}
	b, _ := json.Marshal(m)
	var back Msg
	if err := json.Unmarshal(b, &back); err != nil || back.Cluster == nil || back.Cluster.Charts[0].TimeoutS != 300 || !back.Cluster.ClusterWide {
		t.Fatalf("round trip: %s %+v %v", b, back.Cluster, err)
	}
	local, _ := json.Marshal(Msg{Type: TProvision, LabID: "0123456789ab", Compose: "compose.yaml"})
	if strings.Contains(string(local), "cluster") || strings.Contains(string(local), "contexts") {
		t.Fatalf("laptop provision grew fields: %s", local)
	}
	hello, _ := json.Marshal(Msg{Type: THello, Contexts: []KubeContext{{Name: "kind-dev", Server: "127.0.0.1:6443"}}})
	if !strings.Contains(string(hello), `"contexts":[{"name":"kind-dev","server":"127.0.0.1:6443"}]`) {
		t.Fatalf("hello: %s", hello)
	}
	if !ClusterScopedKinds["CustomResourceDefinition"] || ClusterScopedKinds["Deployment"] {
		t.Fatal("cluster-scoped kinds")
	}
}
