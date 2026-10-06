package labs

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"crucible/internal/awscloud"
	"crucible/internal/content"
)

const (
	DefaultWorkspaceImage = "amazon/aws-cli:2.27.0" // bump deliberately: dind pulls it once per lab
	workspaceCompose      = "crucible-workspace.yaml"
	refreshBefore         = 15 * time.Minute // the sweep refreshes one-hour credentials at about 45 minutes
	tfCredsMargin         = 5 * time.Minute  // a terraform pod starts only with its deadline plus this left
)

// AWSRunner runs runtime: aws labs (spec §8.2). The workspace is a ClusterRunner lab whose only service is the
// workspace container, with the lab's credentials mounted at /aws. One-shot terraform pods in the same namespace
// apply and destroy the lab's module with the same credentials; state lives in the lab account's state bucket under
// labs/<id>.tfstate. Credentials are one-hour STS sessions tagged with the lab id, refreshed by the sweep.
type AWSRunner struct {
	Cluster        *ClusterRunner
	Cloud          awscloud.Cloud
	StateBucket    string // deploy/aws/labs output state_bucket
	StateRegion    string
	WorkspaceImage string
	TerraformImage string
	DryRun         *awscloud.Fake // dry-run mode: terraform pods only unpack the module; this fake stands in for AWS
	Now            func() time.Time

	mu      sync.Mutex
	expires map[string]time.Time   // lab id → when its mounted credentials expire (empty after a restart)
	locks   map[string]*sync.Mutex // one provision/destroy/refresh per lab at a time in this process
}

var _ Runner = (*AWSRunner)(nil)

func (a *AWSRunner) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// ponytail: in-process per-lab lock and expiry map, like setupLocks; a second API replica would need DB state.
func (a *AWSRunner) lock(id string) *sync.Mutex {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.locks == nil {
		a.locks = map[string]*sync.Mutex{}
	}
	if a.locks[id] == nil {
		a.locks[id] = &sync.Mutex{}
	}
	return a.locks[id]
}

func (a *AWSRunner) remember(id string, exp time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.expires == nil {
		a.expires = map[string]time.Time{}
	}
	a.expires[id] = exp
}

func (a *AWSRunner) forget(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.expires, id)
}

// fresh reports whether the mounted credentials are known to last at least `need` longer.
func (a *AWSRunner) fresh(id string, need time.Duration) bool {
	a.mu.Lock()
	exp, ok := a.expires[id]
	a.mu.Unlock()
	return ok && a.now().Add(need).Before(exp)
}

func (a *AWSRunner) Available(*Instance) error { return nil }

func (a *AWSRunner) Provision(context.Context, *Instance, []byte, string) error {
	return errors.New("aws labs are provisioned with ProvisionLab")
}

func (a *AWSRunner) OpenPTY(ctx context.Context, inst *Instance, service string, cols, rows int) (PTY, error) {
	return a.Cluster.OpenPTY(ctx, inst, service, cols, rows)
}

func (a *AWSRunner) RunScript(ctx context.Context, inst *Instance, s ScriptSpec) (ScriptResult, error) {
	return a.Cluster.RunScript(ctx, inst, s)
}

func labSession(inst *Instance) awscloud.Session {
	return awscloud.Session{LabID: inst.ID, Team: inst.Team, Training: inst.Training}
}

// workspaceComposeFile is the aws lab's compose project inside the dind pod: one workspace container with the AWS
// CLI. /aws is the mounted credentials secret; kubelet refreshes it in place, so no restart is needed.
func workspaceComposeFile(image, region, labID string) []byte {
	return fmt.Appendf(nil, `# Written by Crucible: the aws lab workspace (spec §8.2).
services:
  workspace:
    image: %q
    entrypoint: ["sleep", "infinity"]
    working_dir: /root
    environment:
      AWS_SHARED_CREDENTIALS_FILE: /aws/credentials
      AWS_REGION: %q
      CRUCIBLE_LAB_ID: %q
    volumes: ["/aws:/aws:ro"]
`, image, region, labID)
}

func (a *AWSRunner) backend(id string) string {
	return fmt.Sprintf("bucket       = %q\nkey          = %q\nregion       = %q\nuse_lockfile = true\n",
		a.StateBucket, "labs/"+id+".tfstate", a.StateRegion)
}

// putCreds writes the credentials file into the lab's secret, creating or replacing it (RBAC: create, update;
// Crucible can never read a secret back), and remembers when it expires.
func (a *AWSRunner) putCreds(ctx context.Context, id string, c awscloud.Credentials) error {
	ns := labNamespace(id)
	s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: awsSecret, Namespace: ns,
		Labels: map[string]string{labLabel: id}}, Data: map[string][]byte{"credentials": c.File()}}
	secrets := a.Cluster.Client.CoreV1().Secrets(ns)
	_, err := secrets.Create(ctx, s, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		_, err = secrets.Update(ctx, s, metav1.UpdateOptions{})
	}
	if err != nil {
		return err
	}
	a.remember(id, c.Expires)
	return nil
}

// renew assumes the lab role again and mounts the new credentials.
func (a *AWSRunner) renew(ctx context.Context, inst *Instance) error {
	creds, err := a.Cloud.AssumeLab(ctx, labSession(inst))
	if err != nil {
		return fmt.Errorf("getting lab credentials: %w", err)
	}
	return a.putCreds(ctx, inst.ID, creds)
}

// ProvisionLab: lab credentials, the workspace (namespace, policies, credentials secret, module config map, dind pod
// with the workspace container), then terraform apply in a runner pod. Each step is idempotent.
func (a *AWSRunner) ProvisionLab(ctx context.Context, inst *Instance, lab *content.Lab) error {
	if !validLabID(inst.ID) || lab.AWS == nil {
		return errors.New("invalid aws lab")
	}
	l := a.lock(inst.ID)
	l.Lock()
	defer l.Unlock()
	creds, err := a.Cloud.AssumeLab(ctx, labSession(inst))
	if err != nil {
		return fmt.Errorf("getting lab credentials: %w", err)
	}
	module, err := ModuleBundle(lab, inst)
	if err != nil {
		return err
	}
	bundle, err := BundleWith(lab.Dir, map[string][]byte{workspaceCompose: workspaceComposeFile(a.WorkspaceImage, lab.AWS.Region, inst.ID)})
	if err != nil {
		return err
	}
	setup := func(ns string) error {
		if err := a.putCreds(ctx, inst.ID, creds); err != nil {
			return err
		}
		cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: tfConfigMap, Namespace: ns, Labels: map[string]string{labLabel: inst.ID}},
			BinaryData: map[string][]byte{"module.tgz": module}, Data: map[string]string{"backend.hcl": a.backend(inst.ID)}}
		_, err := a.Cluster.Client.CoreV1().ConfigMaps(ns).Create(ctx, cm, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			return nil
		}
		return err
	}
	if err := a.Cluster.provision(ctx, inst, bundle, workspaceCompose, setup); err != nil {
		return err
	}
	if err := a.runTF(ctx, inst, tfApplyPod, "apply"); err != nil {
		return err
	}
	if a.DryRun != nil {
		a.DryRun.SimulateApply(lab.AWS.Region, labSession(inst))
	}
	return nil
}

// runTF runs terraform <action> in a one-shot pod and waits for it. The pod starts only with credentials that
// outlast its deadline. A pod left by an earlier attempt is reused while it runs or after it succeeded; a failed or
// stuck one is deleted (now, or by the next attempt) so a retry runs it again.
func (a *AWSRunner) runTF(ctx context.Context, inst *Instance, name, action string) error {
	ns := labNamespace(inst.ID)
	pods := a.Cluster.Client.CoreV1().Pods(ns)
	pod := tfPod(inst.ID, name, a.TerraformImage, tfScript(action, a.DryRun != nil))
	if !a.fresh(inst.ID, time.Duration(*pod.Spec.ActiveDeadlineSeconds)*time.Second+tfCredsMargin) {
		if err := a.renew(ctx, inst); err != nil {
			return fmt.Errorf("starting terraform %s: %w", action, err)
		}
	}
	_, err := pods.Create(ctx, pod, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		err = nil
		if p, gerr := pods.Get(ctx, name, metav1.GetOptions{}); gerr == nil && (p.Status.Phase == corev1.PodFailed || stuckReason(p)) {
			if err = a.Cluster.deletePod(ctx, ns, name); err == nil {
				_, err = pods.Create(ctx, pod, metav1.CreateOptions{})
			}
		}
	}
	if err != nil {
		return fmt.Errorf("starting terraform %s: %w", action, err)
	}
	ok, msg, err := a.Cluster.waitDone(ctx, ns, name)
	if err != nil {
		return fmt.Errorf("terraform %s: %w", action, err)
	}
	if !ok {
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Minute) // grace period 120s
		defer cancel()
		_ = a.Cluster.deletePod(dctx, ns, name) // best effort: the next attempt deletes it otherwise
		return fmt.Errorf("terraform %s failed: %s", action, tailOf([]byte(msg)))
	}
	return nil
}

func stuckReason(p *corev1.Pod) bool { r, _ := stuck(p); return r != "" }

// Destroy stops a running apply, runs terraform destroy (with fresh credentials), then deletes the namespace.
// Service.destroyRuntime runs the tag sweep afterwards. Without a namespace there is nothing to run terraform in:
// the tag sweep and the reaper clean up instead.
func (a *AWSRunner) Destroy(ctx context.Context, inst *Instance) error {
	if !validLabID(inst.ID) {
		return errors.New("invalid lab id")
	}
	l := a.lock(inst.ID)
	l.Lock()
	defer l.Unlock()
	var errs []error
	if err := a.Cluster.deletePod(ctx, labNamespace(inst.ID), tfApplyPod); err != nil {
		errs = append(errs, fmt.Errorf("stopping terraform apply: %w", err))
	} else if err := a.runTF(ctx, inst, tfDestroyPod, "destroy"); err != nil && !apierrors.IsNotFound(err) {
		errs = append(errs, err)
	} else if err == nil && a.DryRun != nil {
		a.DryRun.SimulateDestroy(inst.ID)
	}
	a.forget(inst.ID)
	if err := a.Cluster.Destroy(ctx, inst); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// Refresh re-assumes the lab role when the mounted credentials have less than refreshBefore left (spec §14: one
// hour, auto-refreshed); kubelet updates the mounted file within about a minute. After a restart nothing is
// remembered, so the first sweep refreshes every ready lab once. A lab busy provisioning or destroying is skipped
// this round: those renew before every terraform pod themselves.
func (a *AWSRunner) Refresh(ctx context.Context, inst *Instance) error {
	if !validLabID(inst.ID) || a.fresh(inst.ID, refreshBefore) {
		return nil
	}
	l := a.lock(inst.ID)
	if !l.TryLock() {
		return nil
	}
	defer l.Unlock()
	return a.renew(ctx, inst)
}
