package ready

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/utils/ptr"
)

const ns = "lab-0123456789ab"

func meta(name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name, Namespace: ns, Generation: 1}
}

func TestParseReadsKindsAndNames(t *testing.T) {
	objs := Parse([]byte("---\n# Source: shop/templates/a.yaml\napiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: shop\n---\nkind: Service\nmetadata: {name: shop}\n---\n"))
	if len(objs) != 2 || objs[0] != (Object{Kind: "Deployment", Name: "shop"}) || objs[1].Kind != "Service" {
		t.Fatalf("%+v", objs)
	}
}

func TestJudge(t *testing.T) {
	cs := fake.NewClientset(
		&appsv1.Deployment{ObjectMeta: meta("up"), Spec: appsv1.DeploymentSpec{Replicas: ptr.To[int32](2)},
			Status: appsv1.DeploymentStatus{ObservedGeneration: 1, UpdatedReplicas: 2, AvailableReplicas: 2}},
		&appsv1.Deployment{ObjectMeta: meta("half"), Spec: appsv1.DeploymentSpec{Replicas: ptr.To[int32](3)},
			Status: appsv1.DeploymentStatus{ObservedGeneration: 1, UpdatedReplicas: 3, AvailableReplicas: 1}},
		&corev1.PersistentVolumeClaim{ObjectMeta: meta("data"), Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimPending}},
		&batchv1.Job{ObjectMeta: meta("migrate"), Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue}}}},
		&corev1.Service{ObjectMeta: meta("web"), Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "web"}}},
		&corev1.Service{ObjectMeta: meta("ok"), Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "ok"}}},
		&discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Name: "ok-x", Namespace: ns, Labels: map[string]string{"kubernetes.io/service-name": "ok"}},
			Endpoints: []discoveryv1.Endpoint{{Conditions: discoveryv1.EndpointConditions{Ready: ptr.To(true)}}}},
	)
	ctx := context.Background()
	if r := Judge(ctx, cs, ns, "a", []Object{{"Deployment", "up"}, {"Service", "ok"}, {"ConfigMap", "c"}}); r.Status != Healthy || len(r.Unready) != 0 {
		t.Fatalf("healthy: %+v", r)
	}
	r := Judge(ctx, cs, ns, "b", []Object{{"Deployment", "half"}, {"PersistentVolumeClaim", "data"}, {"Service", "web"}})
	if r.Status != Progressing || strings.Join(r.Unready, "; ") != "deployment/half: 1 of 3 ready; persistentvolumeclaim/data: Pending; service/web: no ready endpoints" {
		t.Fatalf("progressing: %+v", r)
	}
	if r := Judge(ctx, cs, ns, "c", []Object{{"Job", "migrate"}}); r.Status != Failing || r.Unready[0] != "job/migrate: failed" {
		t.Fatalf("failing: %+v", r)
	}
	if r := Judge(ctx, cs, ns, "d", []Object{{"Deployment", "gone"}}); r.Status != Progressing || r.Unready[0] != "deployment/gone: not found" {
		t.Fatalf("missing: %+v", r)
	}
}

// A rollout stuck on a broken image keeps the old replicas available: "2 of 2 ready" would read as healthy, so the
// message names the updated count instead.
func TestStuckRolloutSaysUpdated(t *testing.T) {
	cs := fake.NewClientset(
		&appsv1.Deployment{ObjectMeta: meta("forge"), Spec: appsv1.DeploymentSpec{Replicas: ptr.To[int32](2)},
			Status: appsv1.DeploymentStatus{ObservedGeneration: 1, UpdatedReplicas: 1, AvailableReplicas: 2}},
		&appsv1.StatefulSet{ObjectMeta: meta("db"), Spec: appsv1.StatefulSetSpec{Replicas: ptr.To[int32](2)},
			Status: appsv1.StatefulSetStatus{UpdatedReplicas: 0, ReadyReplicas: 2}},
		&appsv1.DaemonSet{ObjectMeta: meta("agent"), Status: appsv1.DaemonSetStatus{DesiredNumberScheduled: 3, UpdatedNumberScheduled: 1, NumberReady: 3}},
	)
	r := Judge(context.Background(), cs, ns, "a", []Object{{"Deployment", "forge"}, {"StatefulSet", "db"}, {"DaemonSet", "agent"}})
	if got := strings.Join(r.Unready, "; "); got != "deployment/forge: 1 of 2 updated; statefulset/db: 0 of 2 updated; daemonset/agent: 1 of 3 updated" {
		t.Fatalf("%q", got)
	}
}
