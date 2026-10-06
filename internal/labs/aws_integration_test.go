//go:build cluster

package labs

import (
	"context"
	"os"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"

	"crucible/internal/awscloud"
)

// TestAWSLabEgressOnKind provisions Forge 401's aws lab in dry-run mode on kind (scripts/cluster-check.sh) and checks
// that neither the workspace nor a terraform runner pod can reach IMDS: the node role can assume lab roles, so a lab
// reaching 169.254.169.254 would own the lab account. cluster-check puts a stand-in IMDS on the node (kind has none)
// and proves an unpoliced pod reaches it, so "unreachable" here means the lab's NetworkPolicy blocked it.
func TestAWSLabEgressOnKind(t *testing.T) {
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
	fake := &awscloud.Fake{}
	a := &AWSRunner{Cluster: r, Cloud: fake, DryRun: fake, WorkspaceImage: DefaultWorkspaceImage, TerraformImage: DefaultTerraformImage}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	inst := &Instance{ID: newLabID(), Runtime: "aws"}
	t.Cleanup(func() { _ = a.Destroy(context.Background(), inst) })
	if err := a.ProvisionLab(ctx, inst, cloudHeat(t)); err != nil {
		t.Fatalf("provision: %v", err)
	}

	t.Run("workspace", func(t *testing.T) {
		// amazon/aws-cli has bash but no nc or curl
		reach := func(host, port string) string { return "timeout 5 bash -c 'echo > /dev/tcp/" + host + "/" + port + "'" }
		run := func(script string) ScriptResult {
			res, err := a.RunScript(ctx, inst, ScriptSpec{Service: "workspace", Script: []byte(script), Timeout: 30 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			return res
		}
		if res := run(reach("1.1.1.1", "443")); res.ExitCode != 0 {
			t.Fatalf("a public host must be reachable (control): %+v", res)
		}
		if res := run(reach("169.254.169.254", "80")); res.ExitCode == 0 {
			t.Fatalf("IMDS must be unreachable from the aws workspace: %+v", res)
		}
	})
	t.Run("terraform pod", func(t *testing.T) {
		// The real runner pod spec (uid 65532, read-only root, lab credentials) and its IMDS guard, then probes in
		// place of terraform. Without the guard this pod reached IMDS in its first second (policy not yet enforced).
		script := "set -eu\n" + imdsGuard + "echo | nc -w 4 1.1.1.1 443\nif echo | nc -w 4 169.254.169.254 80; then echo IMDS reachable; exit 1; fi\necho blocked\n"
		pod := tfPod(inst.ID, "egress-probe", DefaultTerraformImage, script)
		if _, err := r.Client.CoreV1().Pods(labNamespace(inst.ID)).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		ok, msg, err := r.waitDone(ctx, labNamespace(inst.ID), "egress-probe")
		if err != nil || !ok { // the log tail lands in msg only on failure
			t.Fatalf("want the public control reachable and IMDS blocked: ok=%v err=%v %s", ok, err, msg)
		}
	})
}
