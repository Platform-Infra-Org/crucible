package labs

import (
	"context"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestAWSWorkspaceObjects(t *testing.T) {
	o := awsWorkspace(clusterObjects(testID, "crucible-workspace.yaml", false))
	c := o.Pod.Spec.Containers[0]
	m, v := c.VolumeMounts[len(c.VolumeMounts)-1], o.Pod.Spec.Volumes[len(o.Pod.Spec.Volumes)-1]
	if m.MountPath != "/aws" || !m.ReadOnly || v.Secret == nil || v.Secret.SecretName != awsSecret {
		t.Fatalf("the workspace mounts the credentials read-only at /aws: %+v %+v", m, v)
	}
	h := o.Quota.Spec.Hard
	if h.Pods().Value() != 2 {
		t.Fatalf("room for the workspace and one terraform pod: %v", h.Pods())
	}
	for k, want := range map[corev1.ResourceName]string{corev1.ResourceLimitsCPU: "3", corev1.ResourceLimitsMemory: "5Gi",
		corev1.ResourceLimitsEphemeralStorage: "24Gi", corev1.ResourceRequestsCPU: "750m"} {
		if got := h[k]; got.Cmp(resource.MustParse(want)) != 0 {
			t.Fatalf("%s = %s, want %s", k, got.String(), want)
		}
	}
	if base := clusterObjects(testID, "compose.yaml", false); base.Quota.Spec.Hard.Pods().Value() != 1 {
		t.Fatal("cluster labs keep their one-pod quota")
	}
}

func TestTFPodIsLockedDown(t *testing.T) {
	p := tfPod(testID, tfApplyPod, DefaultTerraformImage, tfScript("apply", false))
	s, c := p.Spec, p.Spec.Containers[0]
	switch {
	case p.Namespace != labNamespace(testID) || p.Labels[labLabel] != testID:
		t.Fatalf("lives in the lab namespace: %+v", p.ObjectMeta)
	case s.RestartPolicy != corev1.RestartPolicyNever || *s.AutomountServiceAccountToken || *s.EnableServiceLinks:
		t.Fatal("one attempt, no Kubernetes token, no service env")
	case !*s.SecurityContext.RunAsNonRoot || *c.SecurityContext.AllowPrivilegeEscalation || !*c.SecurityContext.ReadOnlyRootFilesystem ||
		c.SecurityContext.Privileged != nil || !slices.Equal(c.SecurityContext.Capabilities.Drop, []corev1.Capability{"ALL"}):
		t.Fatal("non-root, read-only root, no privilege, no capabilities")
	case s.SecurityContext.SeccompProfile == nil || s.SecurityContext.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault:
		t.Fatal("seccomp RuntimeDefault")
	case s.ActiveDeadlineSeconds == nil || *s.ActiveDeadlineSeconds > 3600:
		t.Fatal("a hung run is killed before its one-hour credentials expire")
	case c.TerminationMessagePolicy != corev1.TerminationMessageFallbackToLogsOnError:
		t.Fatal("the log tail must come back through `pods get`")
	case len(c.EnvFrom) != 0 || slices.ContainsFunc(c.Env, func(e corev1.EnvVar) bool { return e.ValueFrom != nil }):
		t.Fatal("credentials come only from the mounted file, never env")
	}
	if s.HostNetwork || s.HostPID || s.HostIPC {
		t.Fatal("no host namespaces")
	}
	if !slices.ContainsFunc(c.Env, func(e corev1.EnvVar) bool { return e.Name == "CHECKPOINT_DISABLE" && e.Value == "1" }) {
		t.Fatal("no checkpoint phone-home")
	}
	if !strings.Contains(DefaultTerraformImage, "@sha256:") {
		t.Fatal("the runner image is pinned by digest")
	}
	var mounts []string
	for _, v := range s.Volumes {
		switch {
		case v.EmptyDir != nil:
		case v.Secret != nil:
			mounts = append(mounts, "secret:"+v.Secret.SecretName)
		case v.ConfigMap != nil:
			mounts = append(mounts, "configmap:"+v.ConfigMap.Name)
		default:
			t.Fatalf("volume %s is neither emptyDir, configMap nor secret", v.Name)
		}
	}
	if !slices.Equal(mounts, []string{"secret:" + awsSecret, "configmap:" + tfConfigMap}) {
		t.Fatalf("volumes %v", mounts)
	}
	if lim := c.Resources.Limits[corev1.ResourceEphemeralStorage]; lim.IsZero() {
		t.Fatal("the lab quota tracks ephemeral storage: every pod needs a limit")
	}
}

func TestTFScripts(t *testing.T) {
	apply, destroy, dry := tfScript("apply", false), tfScript("destroy", false), tfScript("destroy", true)
	if !strings.Contains(apply, "terraform init -input=false -no-color -backend-config=/module/backend.hcl") ||
		!strings.Contains(apply, "terraform apply -input=false -no-color -auto-approve") || strings.Contains(apply, "-lock=false") {
		t.Fatalf("apply:\n%s", apply)
	}
	if !strings.Contains(destroy, "terraform destroy -input=false -no-color -auto-approve -lock=false") {
		t.Fatalf("destroy runs after the apply pod is gone, so a stale lock must not block it:\n%s", destroy)
	}
	for _, sc := range []string{apply, destroy} {
		if strings.Count(sc, "-no-color") != 2 { // init + the action: terraform colours even off a TTY
			t.Fatalf("init and the action need -no-color:\n%s", sc)
		}
		if strings.Count(sc, "-var-file=/w/crucible.auto.tfvars.json") != 1 || strings.Contains(sc, "-force-copy") {
			t.Fatalf("crucible_* variables go on the command line (highest precedence), no state migration:\n%s", sc)
		}
	}
	for _, sc := range []string{apply, destroy, dry} {
		if !strings.HasPrefix(sc, "set -eu\n"+imdsGuard) {
			t.Fatalf("no author code before the lab's NetworkPolicy is seen blocking IMDS:\n%s", sc)
		}
	}
	if strings.Contains(dry, "terraform init") || strings.Contains(dry, "terraform destroy") || !strings.Contains(dry, "terraform version") {
		t.Fatalf("a dry run touches no backend and no cloud:\n%s", dry)
	}
}

func TestProvisionCreatesAWSObjectsBeforeThePod(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewClientset()
	onPodCreate(cs, podReady)
	r := testRunner(cs, &fakeExec{})
	setup := func(ns string) error {
		_, err := cs.CoreV1().Secrets(ns).Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: awsSecret, Namespace: ns}}, metav1.CreateOptions{})
		return err
	}
	if err := r.provision(ctx, &Instance{ID: testID}, []byte("tgz"), "crucible-workspace.yaml", setup); err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, a := range cs.Actions() {
		if a.GetVerb() == "create" {
			order = append(order, a.GetResource().Resource)
		}
	}
	if !slices.Equal(order, []string{"namespaces", "resourcequotas", "limitranges", "networkpolicies", "secrets", "pods"}) {
		t.Fatalf("the secret the pod mounts exists first, behind the policies: %v", order)
	}
	pod, _ := cs.CoreV1().Pods(labNamespace(testID)).Get(ctx, labPod, metav1.GetOptions{})
	if !slices.ContainsFunc(pod.Spec.Volumes, func(v corev1.Volume) bool { return v.Secret != nil }) {
		t.Fatal("aws labs get the workspace mount")
	}
}

func TestWaitDoneAndDeletePod(t *testing.T) {
	ctx, ns := context.Background(), labNamespace(testID)
	pod := func(name string, st corev1.PodStatus) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}, Status: st}
	}
	term := func(phase corev1.PodPhase, msg string) corev1.PodStatus {
		return corev1.PodStatus{Phase: phase, ContainerStatuses: []corev1.ContainerStatus{{State: corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{Message: msg}}}}}
	}
	killed := func(phase corev1.PodPhase, reason, msg string) corev1.PodStatus {
		st := term(phase, msg)
		st.ContainerStatuses[0].State.Terminated.Reason = reason
		return st
	}
	deadline := term(corev1.PodFailed, "tail")
	deadline.Reason = "DeadlineExceeded"
	empty := corev1.PodStatus{Phase: corev1.PodFailed, Reason: "DeadlineExceeded", Message: "Pod was active too long"}
	cs := fake.NewClientset(
		pod("esc", term(corev1.PodFailed, "\x1b[31mError\x1b[0m\x07: bad\n\tdetail\r\x00")),
		pod("oom", killed(corev1.PodFailed, "OOMKilled", "tail")),
		pod("exit", killed(corev1.PodFailed, "Error", "Error: x")),
		pod("dl", deadline),
		pod("empty", empty),
		pod("ok", term(corev1.PodSucceeded, "Apply complete!")),
		pod("bad", term(corev1.PodFailed, "Error: AccessDenied")),
		pod("img", corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{State: corev1.ContainerState{
			Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff", Message: "no such image"}}}}}))
	r := testRunner(cs, &fakeExec{})
	for name, want := range map[string]struct {
		ok  bool
		msg string
	}{"esc": {false, "[31mError[0m: bad\n\tdetail"},
		"oom": {false, "OOMKilled: tail"}, "exit": {false, "Error: x"}, "dl": {false, "DeadlineExceeded: tail"},
		"empty": {false, "DeadlineExceeded Pod was active too long"},
		"ok":    {true, "Apply complete!"}, "bad": {false, "Error: AccessDenied"}, "img": {false, "ImagePullBackOff: no such image"}} {
		ok, msg, err := r.waitDone(ctx, ns, name)
		if err != nil || ok != want.ok || msg != want.msg {
			t.Fatalf("%s: %v %q %v", name, ok, msg, err)
		}
	}
	if err := r.deletePod(ctx, ns, "bad"); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.CoreV1().Pods(ns).Get(ctx, "bad", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("deletePod returns once the pod is gone")
	}
	if err := r.deletePod(ctx, ns, "never-existed"); err != nil {
		t.Fatalf("a missing pod is already deleted: %v", err)
	}
}
