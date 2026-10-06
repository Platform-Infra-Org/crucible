package labs

import (
	"net/netip"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const testID = "0123456789ab"

func TestClusterObjectsSysbox(t *testing.T) {
	o := clusterObjects(testID, "compose.yaml", false)
	if o.Namespace.Name != "lab-"+testID || o.Namespace.Labels[labLabel] != testID ||
		o.Namespace.Labels["pod-security.kubernetes.io/enforce"] != "baseline" {
		t.Fatalf("namespace: %+v", o.Namespace.ObjectMeta)
	}
	for _, m := range []metav1.Object{o.Quota, o.Limits, o.Network, o.Pod} {
		if m.GetNamespace() != o.Namespace.Name || m.GetLabels()[labLabel] != testID {
			t.Fatalf("%s must live in the lab namespace with the lab label", m.GetName())
		}
	}
	sp := o.Pod.Spec
	if sp.RuntimeClassName == nil || *sp.RuntimeClassName != "sysbox-runc" || sp.HostUsers == nil || *sp.HostUsers {
		t.Fatalf("production lab pods use sysbox in a user namespace: %+v %+v", sp.RuntimeClassName, sp.HostUsers)
	}
	if sp.AutomountServiceAccountToken == nil || *sp.AutomountServiceAccountToken || sp.EnableServiceLinks == nil || *sp.EnableServiceLinks {
		t.Fatal("lab pods get no API token and no service env vars")
	}
	if len(sp.Containers) != 1 {
		t.Fatalf("one container: %d", len(sp.Containers))
	}
	c := sp.Containers[0]
	if c.Name != labContainer || c.Image != labImage {
		t.Fatalf("container %s %s", c.Name, c.Image)
	}
	if c.SecurityContext != nil && c.SecurityContext.Privileged != nil && *c.SecurityContext.Privileged {
		t.Fatal("never privileged with sysbox")
	}
	if !slices.Equal(c.Args, []string{"dockerd", "--host=unix:///var/run/docker.sock"}) {
		t.Fatalf("dockerd must listen on the unix socket only: %v", c.Args)
	}
	env := map[string]string{}
	for _, e := range c.Env {
		env[e.Name] = e.Value
	}
	if env["COMPOSE_FILE"] != "/lab/compose.yaml" || env["COMPOSE_PROJECT_NAME"] != "lab" {
		t.Fatalf("compose env: %v", env)
	}
	if c.Resources.Limits.Memory().String() != "4Gi" || c.Resources.Limits.Cpu().String() != "2" {
		t.Fatalf("limits: %v", c.Resources.Limits)
	}
	if o.Quota.Spec.Hard.Pods().Value() != 1 {
		t.Fatalf("quota: %v", o.Quota.Spec.Hard)
	}
}

func TestClusterObjectsPrivilegedDev(t *testing.T) {
	o := clusterObjects(testID, "compose.yaml", true)
	if _, ok := o.Namespace.Labels["pod-security.kubernetes.io/enforce"]; ok {
		t.Fatal("dev namespaces cannot enforce baseline: the pod is privileged")
	}
	sp := o.Pod.Spec
	if sp.RuntimeClassName != nil || sp.HostUsers != nil {
		t.Fatal("kind has no sysbox runtime class and privileged pods need host users")
	}
	if p := sp.Containers[0].SecurityContext; p == nil || p.Privileged == nil || !*p.Privileged {
		t.Fatal("dev dind is privileged")
	}
}

func blockedBy(cidrs []string, ip string) bool {
	a := netip.MustParseAddr(ip)
	for _, c := range cidrs {
		if netip.MustParsePrefix(c).Contains(a) {
			return true
		}
	}
	return false
}

func TestLabNetworkPolicy(t *testing.T) {
	np := clusterObjects(testID, "compose.yaml", false).Network.Spec
	if len(np.PodSelector.MatchLabels) != 0 || len(np.PodSelector.MatchExpressions) != 0 {
		t.Fatal("the policy selects every pod in the namespace")
	}
	if !slices.Contains(np.PolicyTypes, networkingv1.PolicyTypeIngress) || !slices.Contains(np.PolicyTypes, networkingv1.PolicyTypeEgress) || len(np.Ingress) != 0 {
		t.Fatalf("default-deny both ways, no ingress rules: %+v", np)
	}
	if len(np.Egress) != 2 {
		t.Fatalf("exactly DNS + public internet: %+v", np.Egress)
	}
	dns := np.Egress[0]
	if len(dns.To) != 1 || dns.To[0].PodSelector.MatchLabels["k8s-app"] != "kube-dns" ||
		dns.To[0].NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] != "kube-system" || len(dns.Ports) != 2 {
		t.Fatalf("dns rule: %+v", dns)
	}
	for _, p := range dns.Ports {
		if p.Port.IntValue() != 53 || (*p.Protocol != corev1.ProtocolUDP && *p.Protocol != corev1.ProtocolTCP) {
			t.Fatalf("dns port: %+v", p)
		}
	}
	web := np.Egress[1]
	if len(web.Ports) != 0 || len(web.To) != 1 || web.To[0].IPBlock == nil || web.To[0].IPBlock.CIDR != "0.0.0.0/0" {
		t.Fatalf("internet rule: %+v", web)
	}
	except := web.To[0].IPBlock.Except
	for _, ip := range []string{"169.254.169.254", "10.43.0.1", "10.42.0.7", "172.31.5.9", "172.18.0.2", "192.168.1.1", "100.64.0.10"} {
		if !blockedBy(except, ip) {
			t.Errorf("%s (IMDS, cluster, VPC or node) must be blocked", ip)
		}
	}
	if blockedBy(except, "104.16.0.1") || blockedBy(except, "54.230.1.1") {
		t.Error("public registries must stay reachable")
	}
}

// Every lab pod waits until IMDS is unreachable (the NetworkPolicy is in force) before dind starts: the same guard
// as the terraform pods, in the image the node already has, small and unprivileged.
func TestLabPodWaitsForTheIMDSBlock(t *testing.T) {
	for _, privileged := range []bool{false, true} {
		sp := clusterObjects(testID, "compose.yaml", privileged).Pod.Spec
		if len(sp.InitContainers) != 1 {
			t.Fatalf("one init container: %d", len(sp.InitContainers))
		}
		ic := sp.InitContainers[0]
		if ic.Image != labImage || !slices.Equal(ic.Command, []string{"/bin/sh", "-c", imdsGuard}) {
			t.Fatalf("imds guard: %s %v", ic.Image, ic.Command)
		}
		sc := ic.SecurityContext
		if sc == nil || sc.Privileged != nil || sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation ||
			sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem || sc.RunAsNonRoot == nil || !*sc.RunAsNonRoot ||
			sc.Capabilities == nil || !slices.Equal(sc.Capabilities.Drop, []corev1.Capability{"ALL"}) {
			t.Fatalf("minimal security context: %+v", sc)
		}
		for _, r := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory, corev1.ResourceEphemeralStorage} {
			l, q := ic.Resources.Limits[r], ic.Resources.Requests[r]
			if l.IsZero() || q.IsZero() {
				t.Fatalf("%s: requests and limits (the quota counts them): %+v", r, ic.Resources)
			}
		}
		if ic.Resources.Limits.Memory().Cmp(*sp.Containers[0].Resources.Limits.Memory()) >= 0 {
			t.Fatal("the guard is smaller than dind, so the quota does not grow")
		}
	}
}
