package labs

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"crucible/internal/awscloud"
	"crucible/internal/content"
)

func testAWS(cs *fake.Clientset) (*AWSRunner, *awscloud.Fake, *fakeExec) {
	fe, cloud := &fakeExec{}, &awscloud.Fake{}
	return &AWSRunner{Cluster: testRunner(cs, fe), Cloud: cloud, StateBucket: "crucible-444455556666-labstate",
		StateRegion: "eu-west-1", WorkspaceImage: DefaultWorkspaceImage, TerraformImage: DefaultTerraformImage}, cloud, fe
}

// kubelet decides how pods end: the lab pod becomes ready; terraform pods finish with phase and message.
func kubelet(cs *fake.Clientset, phase corev1.PodPhase, msg string) {
	onPodCreate(cs, func(p *corev1.Pod) {
		if p.Name == labPod {
			podReady(p)
			return
		}
		p.Status.Phase = phase
		p.Status.ContainerStatuses = []corev1.ContainerStatus{{State: corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{Message: msg}}}}
	})
}

func cloudHeat(t *testing.T) *content.Lab {
	t.Helper()
	tr, probs := content.Load("../../examples/forge-401")
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	return tr.Module("01-cloud-heat").Lab
}

func untar(t *testing.T, tgz []byte) map[string]string {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(tgz))
	if err != nil {
		t.Fatal(err)
	}
	out, tr := map[string]string{}, tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		out[h.Name] = string(b)
	}
}

func steps(cs *fake.Clientset, verbs ...string) []string {
	var out []string
	for _, a := range cs.Actions() {
		if slices.Contains(verbs, a.GetVerb()) {
			out = append(out, a.GetVerb()+" "+a.GetResource().Resource)
		}
	}
	return out
}

func TestAWSProvisionBuildsTheWorkspaceThenApplies(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewClientset()
	kubelet(cs, corev1.PodSucceeded, "Apply complete!")
	a, cloud, fe := testAWS(cs)
	inst := &Instance{ID: testID, Team: "forge", Training: "forge-401"}
	if err := a.ProvisionLab(ctx, inst, cloudHeat(t)); err != nil {
		t.Fatal(err)
	}
	want := []string{"create namespaces", "create resourcequotas", "create limitranges", "create networkpolicies",
		"create secrets", "create configmaps", "create pods", "create pods"}
	if got := steps(cs, "create"); !slices.Equal(got, want) {
		t.Fatalf("policies, then credentials and module, then the workspace, then terraform: %v", got)
	}
	if s := cloud.Assumed(); len(s) != 1 || s[0] != (awscloud.Session{LabID: testID, Team: "forge", Training: "forge-401"}) {
		t.Fatalf("one tagged session: %+v", s)
	}
	ns := labNamespace(testID)
	sec, _ := cs.CoreV1().Secrets(ns).Get(ctx, awsSecret, metav1.GetOptions{})
	if !strings.Contains(string(sec.Data["credentials"]), "aws_session_token = fake-token-"+testID) {
		t.Fatalf("credentials file: %s", sec.Data["credentials"])
	}
	cm, _ := cs.CoreV1().ConfigMaps(ns).Get(ctx, tfConfigMap, metav1.GetOptions{})
	if !strings.Contains(cm.Data["backend.hcl"], `"labs/`+testID+`.tfstate"`) || !strings.Contains(cm.Data["backend.hcl"], "use_lockfile = true") {
		t.Fatalf("state per lab: %s", cm.Data["backend.hcl"])
	}
	mod := untar(t, cm.BinaryData["module.tgz"])
	if !strings.Contains(mod["main.tf"], "aws_s3_bucket") || mod[content.LabTFFile] != content.LabTF ||
		!strings.Contains(mod[content.LabTFVarsFile], `"crucible_lab_id":"`+testID+`"`) {
		t.Fatalf("module: %v", mod)
	}
	ws := untar(t, fe.calls[0].stdin) // the first exec unpacks the workspace bundle
	compose := ws[workspaceCompose]
	if !strings.Contains(compose, `image: "amazon/aws-cli:2.27.0"`) || !strings.Contains(compose, `AWS_REGION: "eu-west-1"`) ||
		!strings.Contains(compose, `"/aws:/aws:ro"`) {
		t.Fatalf("workspace compose:\n%s", compose)
	}
	for name := range ws {
		if strings.HasPrefix(name, "terraform/") || strings.HasPrefix(name, "checks/") || name == "lab.yaml" {
			t.Fatalf("%s must never reach the workspace", name)
		}
	}
	apply, _ := cs.CoreV1().Pods(ns).Get(ctx, tfApplyPod, metav1.GetOptions{})
	if !strings.Contains(apply.Spec.Containers[0].Command[2], "terraform apply") {
		t.Fatal("the apply pod runs terraform apply")
	}
}

func TestAWSProvisionReportsTerraformFailure(t *testing.T) {
	cs := fake.NewClientset()
	kubelet(cs, corev1.PodFailed, "Error: creating S3 Bucket: AccessDenied")
	a, _, _ := testAWS(cs)
	err := a.ProvisionLab(context.Background(), &Instance{ID: testID}, cloudHeat(t))
	if err == nil || !strings.Contains(err.Error(), "terraform apply failed: Error: creating S3 Bucket: AccessDenied") {
		t.Fatalf("the trainee sees the log tail: %v", err)
	}
	if strings.Contains(err.Error(), "fake-token") || strings.Contains(err.Error(), "fake-secret") {
		t.Fatal("never credentials")
	}
}

func TestAWSDestroyStopsTheApplyFirst(t *testing.T) {
	ctx := context.Background()
	running := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: tfApplyPod, Namespace: labNamespace(testID)},
		Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	cs := fake.NewClientset(labNS(testID, false), running)
	kubelet(cs, corev1.PodSucceeded, "Destroy complete!")
	a, _, _ := testAWS(cs)
	if err := a.Destroy(ctx, &Instance{ID: testID}); err != nil {
		t.Fatal(err)
	}
	want := []string{"delete pods", "create secrets", "create pods", "delete namespaces"}
	if got := steps(cs, "create", "update", "delete"); !slices.Equal(got, want) {
		t.Fatalf("stop apply, fresh credentials, terraform destroy, then the namespace: %v", got)
	}
	d, _ := cs.CoreV1().Pods(labNamespace(testID)).Get(ctx, tfDestroyPod, metav1.GetOptions{})
	if !strings.Contains(d.Spec.Containers[0].Command[2], "-lock=false") {
		t.Fatal("destroy pod")
	}
}

func TestAWSDestroyRetriesAFailedDestroyPodOnce(t *testing.T) {
	ctx := context.Background()
	failed := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: tfDestroyPod, Namespace: labNamespace(testID)},
		Status: corev1.PodStatus{Phase: corev1.PodFailed}}
	cs := fake.NewClientset(labNS(testID, false), failed)
	kubelet(cs, corev1.PodSucceeded, "Destroy complete!")
	a, _, _ := testAWS(cs)
	if err := a.Destroy(ctx, &Instance{ID: testID}); err != nil {
		t.Fatalf("an earlier failed destroy is run again: %v", err)
	}
	if got := steps(cs, "create"); !slices.Equal(got, []string{"create secrets", "create pods", "create pods"}) {
		t.Fatalf("create (exists), delete the failed one, create again: %v", got)
	}
}

func TestAWSDestroyWithoutNamespace(t *testing.T) {
	cs := fake.NewClientset()
	cs.PrependReactor("create", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewNotFound(schema.GroupResource{Resource: "namespaces"}, labNamespace(testID))
	})
	a, _, _ := testAWS(cs)
	if err := a.Destroy(context.Background(), &Instance{ID: testID}); err != nil {
		t.Fatalf("no namespace: nothing to run terraform in; the tag sweep cleans up: %v", err)
	}
	if got := steps(cs, "create"); slices.Contains(got, "create pods") {
		t.Fatalf("no terraform pod without its namespace: %v", got)
	}
}

func TestAWSRefreshOnlyNearExpiry(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	cs := fake.NewClientset(labNS(testID, false))
	a, cloud, _ := testAWS(cs)
	cloud.Now, a.Now = func() time.Time { return now }, func() time.Time { return now }
	inst := &Instance{ID: testID}
	refreshes := func() int { return len(cloud.Assumed()) }
	if err := a.Refresh(ctx, inst); err != nil || refreshes() != 1 {
		t.Fatalf("nothing remembered (fresh process): refresh at once: %v", err)
	}
	now = now.Add(40 * time.Minute) // 20 minutes left
	if _ = a.Refresh(ctx, inst); refreshes() != 1 {
		t.Fatal("no STS call while more than 15 minutes are left")
	}
	now = now.Add(6 * time.Minute) // 14 minutes left
	if err := a.Refresh(ctx, inst); err != nil || refreshes() != 2 || !slices.Contains(steps(cs, "update"), "update secrets") {
		t.Fatalf("refreshed in place: %v %v", err, steps(cs, "create", "update"))
	}
}

func TestAWSDryRunSimulatesTheCloud(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewClientset()
	kubelet(cs, corev1.PodSucceeded, "dry run")
	a, cloud, _ := testAWS(cs)
	a.DryRun = cloud
	if err := a.ProvisionLab(ctx, &Instance{ID: testID}, cloudHeat(t)); err != nil {
		t.Fatal(err)
	}
	bucket, vol := "arn:aws:s3:::crucible-lab-"+testID, "arn:aws:ec2:eu-west-1:000000000000:volume/vol-"+testID
	apply, _ := cs.CoreV1().Pods(labNamespace(testID)).Get(ctx, tfApplyPod, metav1.GetOptions{})
	if !cloud.Has(bucket) || !cloud.Has(vol) || strings.Contains(apply.Spec.Containers[0].Command[2], "terraform apply") {
		t.Fatal("dry run: the fake cloud gets the resources; the pod applies nothing")
	}
	if err := a.Destroy(ctx, &Instance{ID: testID}); err != nil {
		t.Fatal(err)
	}
	if cloud.Has(bucket) || !cloud.Has(vol) {
		t.Fatal("terraform destroy removes its bucket; the hand-made volume is left for the tag sweep")
	}
}

func TestAWSFailedTerraformPodIsDeletedSoARetryRunsAgain(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewClientset()
	kubelet(cs, corev1.PodFailed, "Error: AccessDenied")
	a, _, _ := testAWS(cs)
	if err := a.ProvisionLab(ctx, &Instance{ID: testID}, cloudHeat(t)); err == nil {
		t.Fatal("want failure")
	}
	if _, err := cs.CoreV1().Pods(labNamespace(testID)).Get(ctx, tfApplyPod, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("the failed apply pod is gone: %v", err)
	}
}

func TestAWSUnschedulableTerraformPodFails(t *testing.T) {
	old := unschedulableGrace
	unschedulableGrace = 0
	t.Cleanup(func() { unschedulableGrace = old })
	cs := fake.NewClientset()
	onPodCreate(cs, func(p *corev1.Pod) {
		if p.Name == labPod {
			podReady(p)
			return
		}
		p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse,
			Reason: corev1.PodReasonUnschedulable}}
	})
	a, _, _ := testAWS(cs)
	err := a.ProvisionLab(context.Background(), &Instance{ID: testID}, cloudHeat(t))
	if err == nil || !strings.Contains(err.Error(), "no room") {
		t.Fatalf("a terraform pod that never schedules fails: %v", err)
	}
}

func TestAWSTerraformNeverStartsOnShortCredentials(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	cs := fake.NewClientset(labNS(testID, false))
	kubelet(cs, corev1.PodSucceeded, "Destroy complete!")
	a, cloud, _ := testAWS(cs)
	cloud.Now, a.Now = func() time.Time { return now }, func() time.Time { return now }
	inst := &Instance{ID: testID}
	if err := a.Refresh(ctx, inst); err != nil {
		t.Fatal(err)
	}
	now = now.Add(10 * time.Minute) // 50 minutes left: less than a run's deadline plus margin
	if err := a.Destroy(ctx, inst); err != nil {
		t.Fatal(err)
	}
	if n := len(cloud.Assumed()); n != 2 {
		t.Fatalf("fresh credentials before the terraform pod: %d sessions", n)
	}
}
