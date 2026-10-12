package labs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"crucible/internal/awscloud"
	"crucible/internal/content"
)

const (
	DefaultWorkspaceImage = "amazon/aws-cli:2.27.0@sha256:e3e329e1d2894b7b4bbb0aacacd0a155262159b2e7a3b4275eb1f24046d3e06c" // multi-arch index; bump deliberately: dind pulls it once per lab
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
	creds   map[string]awscloud.Credentials // lab id → its mounted credentials (empty after a restart)
	locks   map[string]chan struct{}        // one provision/destroy per lab at a time in this process
	cancels map[string]*context.CancelFunc  // lab id → stops its running ProvisionLab (Destroy calls it)
}

var _ Runner = (*AWSRunner)(nil)

func (a *AWSRunner) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// acquire takes the lab's lock, giving up when ctx ends. Destroy prunes the lock; a waiter that wakes on a pruned
// lock releases it and takes the current one.
// ponytail: in-process per-lab lock and expiry map, like setupLocks; a second API replica would need DB state.
func (a *AWSRunner) acquire(ctx context.Context, id string) (release func(), err error) {
	for {
		a.mu.Lock()
		if a.locks == nil {
			a.locks = map[string]chan struct{}{}
		}
		l := a.locks[id]
		if l == nil {
			l = make(chan struct{}, 1)
			a.locks[id] = l
		}
		a.mu.Unlock()
		select {
		case l <- struct{}{}: // a free lock wins even over a ctx that is already done
		default:
			select {
			case l <- struct{}{}:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		a.mu.Lock()
		current := a.locks[id] == l
		a.mu.Unlock()
		if current {
			return func() { <-l }, nil
		}
		<-l
	}
}

// prune drops a destroyed lab's lock and remembered expiry.
func (a *AWSRunner) prune(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.locks, id)
	delete(a.creds, id)
}

// cloud is where STS calls go: the dry-run fake whenever it is set, so a wiring slip never reaches real AWS.
func (a *AWSRunner) cloud() awscloud.Cloud {
	if a.DryRun != nil {
		return a.DryRun
	}
	return a.Cloud
}

func (a *AWSRunner) remember(id string, c awscloud.Credentials) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.creds == nil {
		a.creds = map[string]awscloud.Credentials{}
	}
	a.creds[id] = c
}

// masked hides the lab's mounted credential values in a log tail; tailOf masks anything credential-shaped.
func (a *AWSRunner) masked(id, s string) string {
	a.mu.Lock()
	c := a.creds[id]
	a.mu.Unlock()
	for _, v := range []string{c.SessionToken, c.SecretAccessKey, c.AccessKeyID} {
		if v != "" {
			s = strings.ReplaceAll(s, v, "***")
		}
	}
	return s
}

// fresh reports whether the mounted credentials are known to last at least `need` longer.
func (a *AWSRunner) fresh(id string, need time.Duration) bool {
	a.mu.Lock()
	c, ok := a.creds[id]
	a.mu.Unlock()
	return ok && a.now().Add(need).Before(c.Expires)
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
	a.remember(id, c)
	return nil
}

// renew assumes the lab role again and mounts the new credentials.
func (a *AWSRunner) renew(ctx context.Context, inst *Instance) error {
	creds, err := a.cloud().AssumeLab(ctx, labSession(inst))
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
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	a.mu.Lock()
	if a.cancels == nil {
		a.cancels = map[string]*context.CancelFunc{}
	}
	a.cancels[inst.ID] = &cancel
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		if a.cancels[inst.ID] == &cancel {
			delete(a.cancels, inst.ID)
		}
		a.mu.Unlock()
	}()
	release, err := a.acquire(ctx, inst.ID)
	if err != nil {
		return err
	}
	defer release()
	creds, err := a.cloud().AssumeLab(ctx, labSession(inst))
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
		return fmt.Errorf("terraform %s failed: %s", action, tailOf([]byte(a.masked(inst.ID, msg))))
	}
	return nil
}

// moduleExists asks the API server whether the module ConfigMap exists with a server-side dry-run create (RBAC grants
// configmaps create only). Nothing deletes it, and every terraform pod mounts it, so without it no apply ever ran.
// Any other answer (an error, no namespace) counts as "maybe": runTF decides as before.
func (a *AWSRunner) moduleExists(ctx context.Context, id string) bool {
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: tfConfigMap, Namespace: labNamespace(id), Labels: map[string]string{labLabel: id}}}
	_, err := a.Cluster.Client.CoreV1().ConfigMaps(cm.Namespace).Create(ctx, cm, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}})
	return err != nil
}

func stuckReason(p *corev1.Pod) bool { r, _ := stuckOneShot(p); return r != "" }

// Destroy stops a running apply (cancelling an in-process ProvisionLab, then deleting the apply pod), runs terraform
// destroy (with fresh credentials), then deletes the namespace. Service.destroyRuntime runs the tag sweep afterwards.
// Without a namespace, or with one already terminating, there is nothing to run terraform in: the tag sweep and the
// reaper clean up instead, and Destroy returns errTerraformSkipped.
func (a *AWSRunner) Destroy(ctx context.Context, inst *Instance) error {
	if !validLabID(inst.ID) {
		return errors.New("invalid lab id")
	}
	a.mu.Lock()
	if c := a.cancels[inst.ID]; c != nil {
		(*c)()
	}
	a.mu.Unlock()
	release, err := a.acquire(ctx, inst.ID)
	if err != nil {
		return fmt.Errorf("waiting for the lab's provisioning to stop: %w", err)
	}
	defer release()
	gone := func(err error) bool {
		return apierrors.IsNotFound(err) || apierrors.HasStatusCause(err, corev1.NamespaceTerminatingCause)
	}
	var errs []error
	skipped := false
	if err := a.Cluster.deletePod(ctx, labNamespace(inst.ID), tfApplyPod); err != nil {
		errs = append(errs, fmt.Errorf("stopping terraform apply: %w", err))
	} else if !a.moduleExists(ctx, inst.ID) {
		// provisioning failed before the module existed: no apply ever ran, so there is nothing for terraform
	} else if err := a.runTF(ctx, inst, tfDestroyPod, "destroy"); err != nil && !gone(err) {
		errs = append(errs, err)
	} else if err != nil {
		skipped = true
	} else if a.DryRun != nil {
		a.DryRun.SimulateDestroy(inst.ID)
	}
	// Terraform may have used up ctx; the namespace (with live credentials) is deleted regardless.
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	if err := a.Cluster.Destroy(dctx, inst); err != nil {
		errs = append(errs, err)
	}
	a.prune(inst.ID)
	if err := errors.Join(errs...); err != nil || !skipped {
		return err
	}
	return errTerraformSkipped
}

// errTerraformSkipped: Destroy otherwise worked, but terraform destroy could not run (no namespace, or one already
// terminating), so what the apply created runs until the tag sweep and the reaper delete it. destroyRuntime treats it
// as success and leaves the lab's meter running.
var errTerraformSkipped = errors.New("terraform destroy did not run")

// Refresh re-assumes the lab role when the mounted credentials have less than refreshBefore left (spec §14: one
// hour, auto-refreshed); kubelet updates the mounted file within about a minute. After a restart nothing is
// remembered, so the first sweep refreshes every ready lab once. It takes no lab lock, so a long apply never holds it
// up: a concurrent renew only writes another valid session, and in a namespace already deleted or terminating the
// secret write fails. ponytail: a refresh racing Destroy can leave one stale expiry entry; harmless.
func (a *AWSRunner) Refresh(ctx context.Context, inst *Instance) error {
	if !validLabID(inst.ID) || a.fresh(inst.ID, refreshBefore) {
		return nil
	}
	return a.renew(ctx, inst)
}

const maxPlanJSON = 16 << 20

var _ Planner = (*AWSRunner)(nil)

// Plan is an estimate's `terraform plan` of the lab's module (spec 2026-10-12 §2): a one-shot planPod in
// crucible-estimates with the read-only plan role's credentials and nothing else (no lab session, no ops role). It
// returns `terraform show -json`, read from the pod log (at most 16 MiB). The run's credentials, module and pod are
// deleted afterwards. A dry run never plans: it prices from fixed rates.
func (a *AWSRunner) Plan(ctx context.Context, lab *content.Lab) ([]byte, error) {
	if a.DryRun != nil {
		return nil, errors.New("a dry run prices aws labs from fixed rates, not a terraform plan")
	}
	if lab.AWS == nil {
		return nil, errors.New("not an aws lab")
	}
	module, err := planBundle(lab)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, planDeadline+3*time.Minute) // room to schedule; inside the credentials
	defer cancel()
	creds, err := a.Cloud.AssumePlan(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting plan credentials: %w", err)
	}
	core := a.Cluster.Client.CoreV1()
	ns, policy := estimatesObjects()
	if _, err := core.Namespaces().Create(ctx, ns, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return nil, fmt.Errorf("creating %s: %w", estimatesNS, err)
	}
	_, err = a.Cluster.Client.NetworkingV1().NetworkPolicies(estimatesNS).Create(ctx, policy, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return nil, fmt.Errorf("creating %s: %w", estimatesNS, err)
	}
	name := "tf-plan-" + newLabID()
	meta := metav1.ObjectMeta{Name: name, Namespace: estimatesNS, Labels: map[string]string{labLabel: estimatesLabel}}
	defer func() { // best effort, even once ctx is spent; the credentials expire within 15 minutes regardless
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Minute)
		defer cancel()
		_ = a.Cluster.deletePod(dctx, estimatesNS, name)
		_ = core.Secrets(estimatesNS).Delete(dctx, name, metav1.DeleteOptions{})
		_ = core.ConfigMaps(estimatesNS).Delete(dctx, name, metav1.DeleteOptions{})
	}()
	if _, err := core.Secrets(estimatesNS).Create(ctx, &corev1.Secret{ObjectMeta: meta,
		Data: map[string][]byte{"credentials": creds.File()}}, metav1.CreateOptions{}); err != nil {
		return nil, fmt.Errorf("starting terraform plan: %w", err)
	}
	if _, err := core.ConfigMaps(estimatesNS).Create(ctx, &corev1.ConfigMap{ObjectMeta: meta,
		BinaryData: map[string][]byte{"module.tgz": module}}, metav1.CreateOptions{}); err != nil {
		return nil, fmt.Errorf("starting terraform plan: %w", err)
	}
	if _, err := core.Pods(estimatesNS).Create(ctx, planPod(name, a.TerraformImage), metav1.CreateOptions{}); err != nil {
		return nil, fmt.Errorf("starting terraform plan: %w", err)
	}
	ok, msg, err := a.Cluster.waitDone(ctx, estimatesNS, name)
	if err != nil {
		return nil, fmt.Errorf("terraform plan: %w", err)
	}
	if !ok {
		for _, v := range []string{creds.SessionToken, creds.SecretAccessKey, creds.AccessKeyID} {
			if v != "" {
				msg = strings.ReplaceAll(msg, v, "***")
			}
		}
		return nil, &labFault{"terraform plan failed: " + tailOf([]byte(msg))}
	}
	out, err := core.Pods(estimatesNS).GetLogs(name, &corev1.PodLogOptions{Container: "terraform",
		LimitBytes: ptr.To(int64(maxPlanJSON + 1))}).DoRaw(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the plan: %w", err)
	}
	if len(out) > maxPlanJSON {
		return nil, errors.New("the plan's output is over 16 MiB")
	}
	out = bytes.TrimSpace(out)
	return out[bytes.LastIndexByte(out, '\n')+1:], nil
}

// planLeakAge: a plan run older than this is a leftover (Plan gives up after planDeadline plus three minutes).
const planLeakAge = 15 * time.Minute

// reapPlans deletes plan runs' pods, credentials and modules older than planLeakAge: an API that died mid-plan never
// ran Plan's deferred delete. The sweep calls it.
func (a *AWSRunner) reapPlans(ctx context.Context, now time.Time) error {
	core := a.Cluster.Client.CoreV1()
	sel := metav1.ListOptions{LabelSelector: labLabel + "=" + estimatesLabel}
	var errs []error
	reap := func(m metav1.ObjectMeta, del func(context.Context, string, metav1.DeleteOptions) error) {
		if strings.HasPrefix(m.Name, "tf-plan-") && m.CreationTimestamp.Time.Before(now.Add(-planLeakAge)) {
			if err := del(ctx, m.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				errs = append(errs, err)
			}
		}
	}
	pods, err := core.Pods(estimatesNS).List(ctx, sel)
	if err != nil {
		return err
	}
	for _, p := range pods.Items {
		reap(p.ObjectMeta, core.Pods(estimatesNS).Delete)
	}
	secrets, err := core.Secrets(estimatesNS).List(ctx, sel)
	if err != nil {
		return err
	}
	for _, x := range secrets.Items {
		reap(x.ObjectMeta, core.Secrets(estimatesNS).Delete)
	}
	cms, err := core.ConfigMaps(estimatesNS).List(ctx, sel)
	if err != nil {
		return err
	}
	for _, x := range cms.Items {
		reap(x.ObjectMeta, core.ConfigMaps(estimatesNS).Delete)
	}
	return errors.Join(errs...)
}
