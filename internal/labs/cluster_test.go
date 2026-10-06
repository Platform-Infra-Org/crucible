package labs

import (
	"context"
	"errors"

	"crucible/internal/apperr"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/remotecommand"
	utilexec "k8s.io/client-go/util/exec"
)

// completeFirstLab marks module 02 done so the linear training unlocks 03-cluster-heat.
func (f *fx) completeFirstLab(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	for _, it := range []string{"before-the-lab", "lab"} {
		if err := f.s.Learn.SetItem(ctx, f.u.ID, "forge", "forge-101", "02-first-lab", it, "complete", 1); err != nil {
			t.Fatal(err)
		}
	}
}

func TestClusterLabUnavailableWithoutRunner(t *testing.T) {
	f := setup(t, true)
	f.completeFirstLab(t)
	m, err := f.s.ModuleLab(context.Background(), f.u, "forge", "forge-101", "03-cluster-heat")
	if err != nil {
		t.Fatal(err)
	}
	if m.Runtime != "cluster" || m.RuntimeReady || !strings.Contains(m.RuntimeMessage, "cluster labs are not available yet") {
		t.Fatalf("without a cluster runner the lobby must say so: %+v", m)
	}
}

func TestClusterLabChecksAreNotSelfReported(t *testing.T) {
	f := setup(t, true)
	f.completeFirstLab(t)
	f.s.Runners["cluster"], f.s.Estimators["cluster"] = f.run, f.rates
	ctx := context.Background()
	v, err := f.s.Start(ctx, f.u, "forge", "forge-101", "03-cluster-heat")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 200 && v.State != Ready; i++ {
		time.Sleep(10 * time.Millisecond)
		if v, err = f.s.Get(ctx, f.u, v.ID); err != nil {
			t.Fatal(err)
		}
	}
	if v.State != Ready || v.Runtime != "cluster" || v.SelfReported {
		t.Fatalf("cluster lab view: state %s runtime %s self_reported %v", v.State, v.Runtime, v.SelfReported)
	}
	res, err := f.s.Check(ctx, f.u, v.ID, "t1-cast", "")
	if err != nil || !res.Passed {
		t.Fatalf("check: %+v %v", res, err)
	}
	var self bool
	if err := f.s.DB.QueryRow(ctx, `SELECT self_reported FROM check_runs WHERE lab_id = $1`, v.ID).Scan(&self); err != nil {
		t.Fatal(err)
	}
	if self {
		t.Fatal("cluster checks run server-side and must not be flagged self-reported")
	}
	if got := f.run.scripts[len(f.run.scripts)-1]; got.Service != "shell" || !strings.Contains(string(got.Script), "hello crucible") {
		t.Fatalf("the check must run the t1 script in the shell service: %+v", got)
	}
}

// onPodCreate lets a test decide what the "kubelet" reports for the lab pod.
func onPodCreate(cs *fake.Clientset, mutate func(*corev1.Pod)) {
	cs.PrependReactor("create", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
		mutate(a.(k8stesting.CreateAction).GetObject().(*corev1.Pod))
		return false, nil, nil // fall through: the tracker stores the mutated pod
	})
}

func podReady(p *corev1.Pod) {
	p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
}

type execCall struct {
	ns, pod string
	cmd     []string
	stdin   []byte
	tty     bool
}

type fakeExec struct {
	mu    sync.Mutex
	calls []execCall
	fn    func(ctx context.Context, cmd []string, o remotecommand.StreamOptions) error
}

func (f *fakeExec) exec(ctx context.Context, ns, pod string, cmd []string, o remotecommand.StreamOptions) error {
	var in []byte
	if o.Stdin != nil && !o.Tty {
		in, _ = io.ReadAll(o.Stdin)
	}
	f.mu.Lock()
	f.calls = append(f.calls, execCall{ns, pod, cmd, in, o.Tty})
	fn := f.fn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, cmd, o)
	}
	return nil
}

func (f *fakeExec) last() execCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[len(f.calls)-1]
}

func testRunner(cs *fake.Clientset, fe *fakeExec) *ClusterRunner {
	return &ClusterRunner{Client: cs, exec: fe.exec, Poll: time.Millisecond}
}

func TestProvisionCreatesIsolatedLab(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewClientset()
	onPodCreate(cs, podReady)
	fe := &fakeExec{}
	r := testRunner(cs, fe)
	if err := r.Provision(ctx, &Instance{ID: testID}, []byte("tarball"), "compose.yaml"); err != nil {
		t.Fatal(err)
	}
	ns := "lab-" + testID
	var order []string
	for _, a := range cs.Actions() {
		if a.GetVerb() == "create" {
			order = append(order, a.GetResource().Resource)
		}
	}
	if !slices.Equal(order, []string{"namespaces", "resourcequotas", "limitranges", "networkpolicies", "pods"}) {
		t.Fatalf("the policy and quota must exist before the pod: %v", order)
	}
	if len(fe.calls) != 2 {
		t.Fatalf("exec calls: %+v", fe.calls)
	}
	untar, up := fe.calls[0], fe.calls[1]
	if untar.ns != ns || untar.pod != "lab" || !slices.Equal(untar.cmd, []string{"tar", "xzf", "-", "-C", "/lab"}) || string(untar.stdin) != "tarball" {
		t.Fatalf("untar: %+v", untar)
	}
	if !slices.Equal(up.cmd, []string{"docker", "compose", "up", "-d", "--wait", "--quiet-pull"}) {
		t.Fatalf("compose up: %+v", up)
	}
	// a second attempt (sweep retry, double start) is harmless
	if err := r.Provision(ctx, &Instance{ID: testID}, []byte("tarball"), "compose.yaml"); err != nil {
		t.Fatalf("provision must be idempotent: %v", err)
	}
}

func TestProvisionRejectsBadInput(t *testing.T) {
	cs := fake.NewClientset()
	r := testRunner(cs, &fakeExec{})
	for _, tc := range []struct{ id, compose string }{{"../kube-system", "compose.yaml"}, {"ABCDEF012345", "compose.yaml"}, {testID, "../x.yaml"}, {testID, "/etc/x.yaml"}} {
		if err := r.Provision(context.Background(), &Instance{ID: tc.id}, nil, tc.compose); err == nil {
			t.Fatalf("%+v must be refused", tc)
		}
	}
	if len(cs.Actions()) != 0 {
		t.Fatalf("nothing may reach the API for bad input: %v", cs.Actions())
	}
}

func TestProvisionFailsFast(t *testing.T) {
	old := unschedulableGrace
	unschedulableGrace = 0
	t.Cleanup(func() { unschedulableGrace = old })
	cases := map[string]struct {
		mutate func(*corev1.Pod)
		want   string
	}{
		"cluster full": {func(p *corev1.Pod) {
			p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: corev1.PodReasonUnschedulable}}
		}, "no room for another lab"},
		"image": {func(p *corev1.Pod) {
			p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: labContainer,
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff", Message: "pull access denied"}}}}
		}, "could not be started"},
		"dockerd died": {func(p *corev1.Pod) { p.Status.Phase = corev1.PodFailed; p.Status.Message = "dockerd exited" }, "stopped"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cs := fake.NewClientset()
			onPodCreate(cs, tc.mutate)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err := testRunner(cs, &fakeExec{}).Provision(ctx, &Instance{ID: testID}, nil, "compose.yaml")
			if err == nil || !strings.Contains(err.Error(), tc.want) || ctx.Err() != nil {
				t.Fatalf("want a fast %q error, got %v (ctx %v)", tc.want, err, ctx.Err())
			}
		})
	}
}

func TestProvisionReportsComposeFailure(t *testing.T) {
	cs := fake.NewClientset()
	onPodCreate(cs, podReady)
	fe := &fakeExec{fn: func(_ context.Context, cmd []string, o remotecommand.StreamOptions) error {
		if cmd[0] == "docker" {
			_, _ = o.Stdout.Write([]byte("pull access denied for nope"))
			return utilexec.CodeExitError{Err: errors.New("command terminated with exit code 1"), Code: 1}
		}
		return nil
	}}
	err := testRunner(cs, fe).Provision(context.Background(), &Instance{ID: testID}, nil, "compose.yaml")
	if err == nil || !strings.Contains(err.Error(), "docker compose up") || !strings.Contains(err.Error(), "pull access denied") {
		t.Fatalf("the trainee sees the compose log tail: %v", err)
	}
}

func labNS(id string, deleting bool) *corev1.Namespace {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "lab-" + id, Labels: map[string]string{labLabel: id}}}
	if deleting {
		now := metav1.Now()
		ns.DeletionTimestamp, ns.Finalizers = &now, []string{"kubernetes"}
	}
	return ns
}

func TestDestroyAndLive(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewClientset(labNS(testID, false), labNS("bbbbbbbbbbbb", true),
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "sneaky", Labels: map[string]string{labLabel: "cccccccccccc"}}})
	r := testRunner(cs, &fakeExec{})
	ids, err := r.Live(ctx)
	if err != nil || !slices.Equal(ids, []string{testID}) {
		t.Fatalf("live = running lab namespaces only (not terminating, not mislabelled): %v %v", ids, err)
	}
	if err := r.Destroy(ctx, &Instance{ID: testID}); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.CoreV1().Namespaces().Get(ctx, "lab-"+testID, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("namespace must be gone: %v", err)
	}
	if err := r.Destroy(ctx, &Instance{ID: testID}); err != nil {
		t.Fatalf("destroying twice is fine: %v", err)
	}
	cs.PrependReactor("delete", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewConflict(corev1.Resource("namespaces"), "lab-"+testID, errors.New("namespace is terminating"))
	})
	if err := r.Destroy(ctx, &Instance{ID: testID}); err != nil {
		t.Fatalf("a namespace still terminating is already being destroyed: %v", err)
	}
	if err := r.Destroy(ctx, &Instance{ID: "default"}); err == nil {
		t.Fatal("only lab ids may be destroyed")
	}
	if _, err := cs.CoreV1().Namespaces().Get(ctx, "default", metav1.GetOptions{}); err != nil {
		t.Fatal("default must survive")
	}
}

func TestSweepRemovesOrphanLabNamespaces(t *testing.T) {
	f := setup(t, true)
	ctx := context.Background()
	now := f.clk.Now()
	inst := func(module string, st State) *Instance {
		return &Instance{ID: newLabID(), UserID: f.u.ID, Team: "forge", Training: "forge-101", Module: module, SHA: "abc",
			Runtime: "cluster", State: st, CreatedAt: now, LastActivityAt: now, TTL: time.Hour, IdleTimeout: 30 * time.Minute, Tier: "auto"}
	}
	live, over := inst("03-cluster-heat", Ready), inst("02-first-lab", Destroyed)
	for _, in := range []*Instance{live, over} {
		if err := f.s.insert(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	stranger := newLabID() // a namespace with no lab row at all
	cs := fake.NewClientset(labNS(live.ID, false), labNS(over.ID, false), labNS(stranger, false))
	cr := &ClusterRunner{Client: cs}
	f.s.Runners["cluster"] = cr
	f.s.Sweep(ctx)
	ids, err := cr.Live(ctx)
	if err != nil || !slices.Equal(ids, []string{live.ID}) {
		t.Fatalf("only the running lab keeps its namespace: %v %v", ids, err)
	}
}

func TestApproveWithoutClusterRunnerStaysPending(t *testing.T) {
	f := setup(t, true)
	ctx := context.Background()
	now := f.clk.Now()
	esc := now.Add(time.Hour)
	in := &Instance{ID: newLabID(), UserID: f.u.ID, Team: "forge", Training: "forge-101", Module: "03-cluster-heat", SHA: "abc",
		Runtime: "cluster", State: PendingApproval, CreatedAt: now, LastActivityAt: now, TTL: time.Hour, IdleTimeout: 30 * time.Minute,
		Tier: "approver", EscalateAt: &esc}
	if err := f.s.insert(ctx, in); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Decide(ctx, f.leader, in.ID, true, ""); !errors.Is(err, apperr.Unavailable) {
		t.Fatalf("want Unavailable, got %v", err)
	}
	var st string
	if err := f.s.DB.QueryRow(ctx, `SELECT state FROM lab_instances WHERE id = $1`, in.ID).Scan(&st); err != nil || st != string(PendingApproval) {
		t.Fatalf("must stay pending: %q %v", st, err)
	}
}

func TestSweepSkipsClusterRowsWithoutRunner(t *testing.T) {
	f := setup(t, true)
	ctx := context.Background()
	now := f.clk.Now()
	past := now.Add(-time.Minute)
	mk := func(module, rt string) *Instance {
		in := &Instance{ID: newLabID(), UserID: f.u.ID, Team: "forge", Training: "forge-101", Module: module, SHA: "abc",
			Runtime: rt, State: Ready, CreatedAt: now, LastActivityAt: now, TTL: time.Hour, IdleTimeout: 30 * time.Minute, Tier: "auto", EndsAt: &past}
		if err := f.s.insert(ctx, in); err != nil {
			t.Fatal(err)
		}
		if _, err := f.s.DB.Exec(ctx, `UPDATE lab_instances SET ends_at = $2 WHERE id = $1`, in.ID, past); err != nil {
			t.Fatal(err) // insert doesn't store ends_at
		}
		return in
	}
	c, l := mk("03-cluster-heat", "cluster"), mk("02-first-lab", "local")
	f.s.Sweep(ctx)
	for _, id := range []string{c.ID, l.ID} {
		var st string
		if err := f.s.DB.QueryRow(ctx, `SELECT state FROM lab_instances WHERE id = $1`, id).Scan(&st); err != nil || st != string(Destroyed) {
			t.Fatalf("%s: %q %v", id, st, err)
		}
	}
}

func TestTailOfIsStorable(t *testing.T) {
	in := strings.Repeat("é", 1500) + "\x00end" // 3000+ bytes: the cut can land inside a rune
	for _, n := range []int{0, 1} {
		got := tailOf([]byte(in[n:]))
		if !utf8.ValidString(got) || strings.ContainsRune(got, 0) || !strings.HasSuffix(got, "end") {
			t.Fatalf("tailOf not storable: %q", got)
		}
	}
}

func TestLabObjectsPinsAndReserves(t *testing.T) {
	p := clusterObjects(testID, "compose.yaml", false).Pod.Spec.Containers[0]
	if !strings.Contains(p.Image, "@sha256:") {
		t.Fatalf("dind image must be pinned by digest: %s", p.Image)
	}
	if p.Resources.Requests.StorageEphemeral().IsZero() {
		t.Fatal("the lab must request ephemeral storage so the scheduler counts disk")
	}
}

func TestLabPodStoppedByTheIMDSGuardSaysWhy(t *testing.T) {
	cs := fake.NewClientset(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: labPod, Namespace: "lab-x"}, Status: corev1.PodStatus{
		Phase: corev1.PodFailed, InitContainerStatuses: []corev1.ContainerStatus{{Name: "imds-guard", State: corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Message: "IMDS is reachable: the lab NetworkPolicy is not in force"}}}}}})
	err := testRunner(cs, &fakeExec{}).waitReady(context.Background(), "lab-x")
	if err == nil || !strings.Contains(err.Error(), "imds-guard: IMDS is reachable") {
		t.Fatalf("%v", err)
	}
}
