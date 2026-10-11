package labs

import (
	"context"
	"errors"

	ap "crucible/internal/agentproto"
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
	f.s.Kube = cr
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
		f.waitState(t, f.u, id, Destroyed) // a cluster destroy runs in the background
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

func TestClusterLabStartsOnAPickedContextForFree(t *testing.T) {
	f := setup(t, true)
	f.completeFirstLab(t)
	ctx := context.Background()
	contexts := []ap.KubeContext{{Name: "kind-dev"}}
	f.s.Runners["cluster"] = AgentClusterRunner{Toolbox: "t", contexts: func(int64) []ap.KubeContext { return contexts },
		online: func(int64) bool { return true }, call: func(context.Context, int64, ap.Msg) (ap.Msg, error) { return ap.Msg{}, nil }}
	f.s.Estimators["cluster"] = FixedRates{}
	if _, err := f.s.Start(ctx, f.u, "forge", "forge-101", "03-cluster-heat", StartOptions{}); err == nil || !strings.Contains(err.Error(), "pick a cluster") {
		t.Fatalf("no context: %v", err)
	}
	v, err := f.s.Start(ctx, f.u, "forge", "forge-101", "03-cluster-heat", StartOptions{Context: "kind-dev"})
	if err != nil || v.ClusterContext != "kind-dev" || v.EstimateUSD != 0 || v.Tier != "auto" {
		t.Fatalf("start: %+v %v", v, err)
	}
	if _, err := f.s.Start(ctx, f.u, "forge", "forge-101", "02-first-lab", StartOptions{Context: "kind-dev"}); err == nil {
		t.Fatal("a laptop lab must refuse a context")
	}
	// starting again while it is active returns the same lab, even with no (or a stale) context
	again, err := f.s.Start(ctx, f.u, "forge", "forge-101", "03-cluster-heat", StartOptions{})
	if err != nil || again.ID != v.ID {
		t.Fatalf("restart of an active lab: %+v %v", again, err)
	}
}

func TestClusterLabHealthAfterProvisionAndChecks(t *testing.T) {
	f := setup(t, true)
	f.completeFirstLab(t)
	ctx := context.Background()
	var scripts []string
	f.s.Runners["cluster"] = AgentClusterRunner{Toolbox: "t", contexts: func(int64) []ap.KubeContext { return []ap.KubeContext{{Name: "k"}} },
		online: func(int64) bool { return true },
		call: func(_ context.Context, _ int64, m ap.Msg) (ap.Msg, error) {
			if m.Type == ap.TRunScript {
				scripts = append(scripts, string(m.Data))
				if strings.Contains(string(m.Data), "--json") {
					return ap.Msg{Data: []byte(`{"releases":[{"name":"forge","status":"progressing","unready":["deployment/forge: 1 of 2 ready"]}]}`)}, nil
				}
			}
			return ap.Msg{}, nil
		}}
	f.s.Estimators["cluster"] = FixedRates{}
	v, err := f.s.Start(ctx, f.u, "forge", "forge-101", "03-cluster-heat", StartOptions{Context: "k"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 200 && v.State != Ready; i++ {
		time.Sleep(10 * time.Millisecond)
		if v, err = f.s.Get(ctx, f.u, v.ID); err != nil {
			t.Fatal(err)
		}
	}
	if v.Health == nil || v.Health.Releases[0].Status != "progressing" || !v.SelfReported {
		t.Fatalf("health after provision: %+v self-reported %v", v.Health, v.SelfReported)
	}
	if !slices.ContainsFunc(scripts, func(s string) bool { return strings.Contains(s, "crucible-ready --json all") }) {
		t.Fatalf("no health run: %q", scripts)
	}
}

// slowCluster is a cluster runner whose destroy (helm uninstall --wait, a namespace delete) takes until hold closes.
type slowCluster struct {
	AgentClusterRunner
	hold chan struct{}
}

func (s slowCluster) Destroy(context.Context, *Instance) error { <-s.hold; return nil }

// A cluster destroy can take minutes on the agent: End, the sweep and the kill switch (which holds up AWS destroys)
// must not wait for it.
func TestClusterDestroyDoesNotHoldUpTheCaller(t *testing.T) {
	f := setup(t, true)
	f.completeFirstLab(t)
	ctx := context.Background()
	run := slowCluster{AgentClusterRunner{Toolbox: "t", contexts: func(int64) []ap.KubeContext { return []ap.KubeContext{{Name: "k"}} },
		online: func(int64) bool { return true }, call: func(context.Context, int64, ap.Msg) (ap.Msg, error) { return ap.Msg{}, nil }},
		make(chan struct{})}
	f.s.Runners["cluster"] = run
	f.s.Estimators["cluster"] = FixedRates{}
	v, err := f.s.Start(ctx, f.u, "forge", "forge-101", "03-cluster-heat", StartOptions{Context: "k"})
	if err != nil {
		t.Fatal(err)
	}
	f.waitState(t, f.u, v.ID, Ready)
	done := make(chan *View, 1)
	go func() { ended, _ := f.s.End(ctx, f.u, v.ID); done <- ended }()
	select {
	case ended := <-done:
		if ended == nil || ended.State != Destroying {
			t.Fatalf("End: %+v", ended)
		}
	case <-time.After(5 * time.Second):
		close(run.hold)
		t.Fatal("End waited for the cluster destroy")
	}
	// a stuck destroy's retry in the sweep doesn't wait either
	if _, err := f.s.DB.Exec(ctx, `UPDATE lab_instances SET destroyed_at = $2 WHERE id = $1`, v.ID, f.clk.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	swept := make(chan struct{})
	go func() { f.s.Sweep(ctx); close(swept) }()
	select {
	case <-swept:
	case <-time.After(5 * time.Second):
		close(run.hold)
		t.Fatal("the sweep waited for the cluster destroy")
	}
	close(run.hold)
	f.waitState(t, f.u, v.ID, Destroyed)
	if destroyTimeout("cluster") < 7*time.Minute {
		t.Fatal("the server gives up before the agent's helm uninstall and namespace delete (5m) can finish")
	}
}
