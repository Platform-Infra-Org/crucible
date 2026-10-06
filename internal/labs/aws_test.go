package labs

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
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

// applyRunning starts ProvisionLab with the apply pod kept Running (a destroy pod succeeds) and returns once the
// apply pod exists, with a channel for ProvisionLab's result.
func applyRunning(t *testing.T, a *AWSRunner, cs *fake.Clientset, ctx context.Context) <-chan error {
	t.Helper()
	onPodCreate(cs, func(p *corev1.Pod) {
		switch p.Name {
		case labPod:
			podReady(p)
		case tfApplyPod:
			p.Status.Phase = corev1.PodRunning
		default:
			p.Status.Phase = corev1.PodSucceeded
		}
	})
	done := make(chan error, 1)
	go func() { done <- a.ProvisionLab(ctx, &Instance{ID: testID}, cloudHeat(t)) }()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		if _, err := cs.CoreV1().Pods(labNamespace(testID)).Get(ctx, tfApplyPod, metav1.GetOptions{}); err == nil {
			return done
		}
		if time.Now().After(deadline) {
			t.Fatal("the apply pod never started")
		}
	}
}

func TestAWSDestroyInterruptsARunningApply(t *testing.T) {
	cs := fake.NewClientset()
	a, _, _ := testAWS(cs)
	provisioned := applyRunning(t, a, cs, context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := a.Destroy(ctx, &Instance{ID: testID}); err != nil {
		t.Fatalf("End during apply: the apply stops and terraform destroy runs: %v", err)
	}
	if err := <-provisioned; err == nil {
		t.Fatal("the interrupted provision fails")
	}
	got := steps(cs, "delete", "create")
	if i := slices.Index(got, "delete pods"); i < 0 || !slices.Contains(got[i:], "create pods") {
		t.Fatalf("the apply pod is deleted before terraform destroy starts: %v", got)
	}
	if _, err := cs.CoreV1().Pods(labNamespace(testID)).Get(ctx, tfApplyPod, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("the apply pod is gone: %v", err)
	}
	if len(a.locks) != 0 {
		t.Fatal("a destroyed lab's lock is pruned")
	}
}

func TestAWSRefreshIsNotBlockedByALongApply(t *testing.T) {
	now := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	cs := fake.NewClientset()
	a, cloud, _ := testAWS(cs)
	var mu sync.Mutex
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	cloud.Now, a.Now = clock, clock
	ctx, cancel := context.WithCancel(context.Background())
	provisioned := applyRunning(t, a, cs, ctx)
	defer func() { cancel(); <-provisioned }()
	mu.Lock()
	now = now.Add(50 * time.Minute)
	mu.Unlock()
	if err := a.Refresh(context.Background(), &Instance{ID: testID}); err != nil || len(cloud.Assumed()) != 2 {
		t.Fatalf("the workspace credentials are renewed during the apply: %v, %d sessions", err, len(cloud.Assumed()))
	}
}

func TestAWSLockWaiterOnAPrunedLockTakesTheNewOne(t *testing.T) {
	ctx := context.Background()
	a := &AWSRunner{}
	release, err := a.acquire(ctx, testID)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan func(), 1)
	go func() { r, _ := a.acquire(ctx, testID); got <- r }()
	time.Sleep(10 * time.Millisecond) // the waiter is parked on the old lock
	a.prune(testID)
	release()
	second := <-got
	short, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if _, err := a.acquire(short, testID); err == nil {
		t.Fatal("the waiter holds the current lock: a third caller waits")
	}
	second()
}

// ctxClient makes namespace deletes honour their ctx, as the real API client does.
type ctxClient struct{ *fake.Clientset }
type ctxCore struct{ typedcorev1.CoreV1Interface }
type ctxNamespaces struct{ typedcorev1.NamespaceInterface }

func (c ctxClient) CoreV1() typedcorev1.CoreV1Interface { return ctxCore{c.Clientset.CoreV1()} }
func (c ctxCore) Namespaces() typedcorev1.NamespaceInterface {
	return ctxNamespaces{c.CoreV1Interface.Namespaces()}
}
func (n ctxNamespaces) Delete(ctx context.Context, name string, o metav1.DeleteOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return n.NamespaceInterface.Delete(ctx, name, o)
}

func TestAWSDestroyDeletesTheNamespaceAfterTheCallerGaveUp(t *testing.T) {
	cs := fake.NewClientset(labNS(testID, false))
	kubelet(cs, corev1.PodSucceeded, "Destroy complete!")
	a, _, _ := testAWS(cs)
	a.Cluster.Client = ctxClient{cs}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = a.Destroy(ctx, &Instance{ID: testID})
	if !slices.Contains(steps(cs, "delete"), "delete namespaces") {
		t.Fatal("the namespace (with live credentials) is deleted even when the caller's ctx is over")
	}
}

func TestAWSDryRunNeverCallsTheRealCloud(t *testing.T) {
	cs := fake.NewClientset()
	kubelet(cs, corev1.PodSucceeded, "dry run")
	a, real, _ := testAWS(cs)
	dry := &awscloud.Fake{}
	a.DryRun = dry
	if err := a.ProvisionLab(context.Background(), &Instance{ID: testID}, cloudHeat(t)); err != nil {
		t.Fatal(err)
	}
	if len(real.Assumed()) != 0 || len(dry.Assumed()) != 1 {
		t.Fatalf("dry run: STS goes to the fake only: real %d, fake %d", len(real.Assumed()), len(dry.Assumed()))
	}
}

func TestAWSDestroyOfATerminatingNamespace(t *testing.T) {
	cs := fake.NewClientset(labNS(testID, true))
	cs.PrependReactor("create", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, &apierrors.StatusError{ErrStatus: metav1.Status{Status: metav1.StatusFailure, Code: 403,
			Reason: metav1.StatusReasonForbidden, Details: &metav1.StatusDetails{
				Causes: []metav1.StatusCause{{Type: corev1.NamespaceTerminatingCause}}}}}
	})
	cs.PrependReactor("delete", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "namespaces"}, labNamespace(testID), nil)
	})
	a, _, _ := testAWS(cs)
	if err := a.Destroy(context.Background(), &Instance{ID: testID}); err != nil {
		t.Fatalf("a namespace already being deleted is already being destroyed: %v", err)
	}
}

func TestAWSTerraformPodThatNeverMountsIsStuck(t *testing.T) {
	cs := fake.NewClientset()
	onPodCreate(cs, func(p *corev1.Pod) {
		if p.Name == labPod {
			podReady(p)
			return
		}
		p.CreationTimestamp = metav1.NewTime(time.Now().Add(-4 * time.Minute))
		p.Status.Phase = corev1.PodPending
		p.Status.ContainerStatuses = []corev1.ContainerStatus{{State: corev1.ContainerState{
			Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"}}}}
	})
	a, _, _ := testAWS(cs)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := a.ProvisionLab(ctx, &Instance{ID: testID}, cloudHeat(t))
	if err == nil || !strings.Contains(err.Error(), "ContainerCreating") {
		t.Fatalf("a terraform pod whose volumes never mount fails: %v", err)
	}
}

func TestAWSTerraformLogTailNeverCarriesCredentials(t *testing.T) {
	realish := "[default]\naws_access_key_id = ASIAQWERTYUIOPASDFGH\naws_secret_access_key = wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY\n" +
		"aws_session_token = " + strings.Repeat("IQoJb3JpZ2luX2VjEJr//////////wEaCXVzLWVhc3QtMSJHMEUCIQ", 6) + "==\n" +
		"export AWS_SECRET_ACCESS_KEY=\"abcdEFGHijklMNOPqrstUVWXyz0123456789+/ab\"\nkey AKIAIOSFODNN7EXAMPLE in a sentence\n"
	cs := fake.NewClientset()
	// local-exec { command = "cat $AWS_SHARED_CREDENTIALS_FILE; exit 1" }, with the lab's own credentials too
	cloud := &awscloud.Fake{}
	own, _ := cloud.AssumeLab(context.Background(), awscloud.Session{LabID: testID})
	kubelet(cs, corev1.PodFailed, realish+"bare: "+own.SecretAccessKey+" "+own.SessionToken+" "+own.AccessKeyID+
		"\nError: local-exec provisioner error")
	a, _, _ := testAWS(cs)
	err := a.ProvisionLab(context.Background(), &Instance{ID: testID}, cloudHeat(t))
	if err == nil || !strings.Contains(err.Error(), "Error: local-exec provisioner error") {
		t.Fatalf("the tail is still shown: %v", err)
	}
	for _, secret := range []string{"ASIAQWERTYUIOPASDFGH", "wJalrXUtnFEMI", "IQoJb3JpZ2lu", "abcdEFGHijkl", "AKIAIOSFODNN7EXAMPLE",
		own.SecretAccessKey, own.SessionToken, own.AccessKeyID} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("%q leaked: %v", secret, err)
		}
	}
}
