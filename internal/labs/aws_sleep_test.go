package labs

import (
	"context"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/remotecommand"

	"crucible/internal/awscloud"
)

func TestAWSWorkspaceKeepsItsDiskOnAVolume(t *testing.T) {
	o := awsWorkspace(clusterObjects(testID, workspaceCompose, false))
	if o.PVC == nil || o.PVC.Name != "workspace" || o.PVC.Spec.Resources.Requests.Storage().String() != "20Gi" {
		t.Fatalf("one 20 GiB volume per workspace: %+v", o.PVC)
	}
	mounts := map[string]string{}
	for _, m := range o.Pod.Spec.Containers[0].VolumeMounts {
		mounts[m.MountPath] = m.Name + ":" + m.SubPath
	}
	if mounts["/var/lib/docker"] != "workspace:docker" || mounts["/lab"] != "workspace:lab" {
		t.Fatalf("Docker's storage and /lab live on the volume: %v", mounts)
	}
	for _, v := range o.Pod.Spec.Volumes {
		if v.EmptyDir != nil {
			t.Fatalf("nothing the trainee keeps is on an emptyDir: %s", v.Name)
		}
	}
	if q := o.Quota.Spec.Hard[corev1.ResourcePersistentVolumeClaims]; q.String() != "1" {
		t.Fatalf("the quota allows that one volume: %v", q.String())
	}
	if c := clusterObjects(testID, "compose.yaml", false); c.PVC != nil {
		t.Fatal("other lab namespaces keep their emptyDirs")
	}
}

func TestSleepLabStopsTheWorkspaceThenTheMachines(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewClientset(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: labPod, Namespace: labNamespace(testID)}})
	a, cloud, fe := testAWS(cs)
	cloud.AddInstance("eu-west-1", testID, "i-1", false)
	how, err := a.SleepLab(ctx, &Instance{ID: testID, Team: "forge", Training: "forge-401"}, cloudHeat(t))
	if err != nil || how != "stopped" {
		t.Fatalf("%q %v", how, err)
	}
	if got := strings.Join(fe.last().cmd, " "); got != "docker compose stop" {
		t.Fatalf("the workspace's containers stop cleanly first: %s", got)
	}
	if _, err := cs.CoreV1().Pods(labNamespace(testID)).Get(ctx, labPod, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("the workspace pod goes; its volume stays")
	}
	if cloud.InstanceState("i-1") != "stopped" {
		t.Fatal("the lab's machine sleeps")
	}
	cloud.AddInstance("eu-west-1", testID, "i-2", true)
	cloud.AddInstance("eu-west-1", testID, "i-1", true) // running again, and this time launched for hibernation
	if how, err := a.SleepLab(ctx, &Instance{ID: testID}, cloudHeat(t)); err != nil || how != "hibernated" {
		t.Fatalf("every machine hibernated: %q %v", how, err)
	}
}

func TestWakeLabStartsTheMachinesAndTheSameContainer(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewClientset()
	kubelet(cs, corev1.PodSucceeded, "")
	a, cloud, fe := testAWS(cs)
	ns := labNamespace(testID)
	cloud.AddInstance("eu-west-1", testID, "i-1", false)
	c, _ := cloud.AssumeLab(ctx, awscloud.Session{LabID: testID})
	if _, err := cloud.Sleep(ctx, "eu-west-1", c, testID); err != nil {
		t.Fatal(err)
	}
	if err := a.WakeLab(ctx, &Instance{ID: testID, Team: "forge", Training: "forge-401"}, cloudHeat(t)); err != nil {
		t.Fatal(err)
	}
	if cloud.InstanceState("i-1") != "running" {
		t.Fatal("the machine runs again")
	}
	p, err := cs.CoreV1().Pods(ns).Get(ctx, labPod, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(p.Spec.Volumes, func(v corev1.Volume) bool {
		return v.PersistentVolumeClaim != nil && v.PersistentVolumeClaim.ClaimName == "workspace"
	}) {
		t.Fatal("the pod comes back on the same volume")
	}
	if got := strings.Join(fe.last().cmd, " "); got != "docker compose up -d --no-recreate --wait --quiet-pull" {
		t.Fatalf("the trainee's own container starts again, never a new one: %s", got)
	}
	if !slices.Contains(steps(cs, "create", "update"), "create secrets") {
		t.Fatal("fresh credentials are mounted")
	}
}

func TestWorkspaceMiBAddsUpTheVolume(t *testing.T) {
	cs := fake.NewClientset()
	a, _, fe := testAWS(cs)
	fe.fn = func(_ context.Context, cmd []string, o remotecommand.StreamOptions) error {
		_, _ = o.Stdout.Write([]byte("1500\t/var/lib/docker\n24\t/lab\n"))
		return nil
	}
	if n, err := a.WorkspaceMiB(context.Background(), &Instance{ID: testID}); err != nil || n != 1524 {
		t.Fatalf("%d %v", n, err)
	}
	if got := strings.Join(fe.last().cmd, " "); got != "du -sxm /var/lib/docker /lab" {
		t.Fatalf("measured with du: %s", got)
	}
}
