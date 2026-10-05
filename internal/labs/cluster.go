package labs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/utils/ptr"

	ap "crucible/internal/agentproto"
)

// ClusterRunner runs each lab as one pod in its own namespace lab-<id> (spec §8.2). dockerd runs the lab's compose
// file in the pod (sysbox runtime: never privileged in production). Terminals, checks and setups are exec calls
// through the Kubernetes API, so results come from the server, never from the trainee's machine.
type ClusterRunner struct {
	Client     kubernetes.Interface
	Config     *rest.Config  // used by real exec; unit tests set exec instead
	Privileged bool          // dev clusters without sysbox only (CRUCIBLE_CLUSTER_PRIVILEGED=1)
	Poll       time.Duration // pod readiness poll interval; 0 = 2s
	exec       execFunc      // nil = realExec
}

type execFunc func(ctx context.Context, ns, pod string, cmd []string, o remotecommand.StreamOptions) error

// unschedulableGrace is how long a lab pod may wait for room before provisioning fails (a var for tests).
var unschedulableGrace = 2 * time.Minute

var errClusterFull = errors.New("the lab cluster has no room for another lab right now; try again in a few minutes")

func NewClusterRunner(cfg *rest.Config, privileged bool) (*ClusterRunner, error) {
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &ClusterRunner{Client: cs, Config: cfg, Privileged: privileged}, nil
}

// Available: the cluster is always there; capacity is checked when the pod is scheduled (errClusterFull).
func (c *ClusterRunner) Available(*Instance) error { return nil }

func (c *ClusterRunner) Provision(ctx context.Context, inst *Instance, bundle []byte, compose string) error {
	if !validLabID(inst.ID) || !filepath.IsLocal(compose) {
		return errors.New("invalid lab id or compose file name")
	}
	o := clusterObjects(inst.ID, compose, c.Privileged)
	core, ns := c.Client.CoreV1(), o.Namespace.Name
	create := []func() error{
		func() error { _, err := core.Namespaces().Create(ctx, o.Namespace, metav1.CreateOptions{}); return err },
		func() error {
			_, err := core.ResourceQuotas(ns).Create(ctx, o.Quota, metav1.CreateOptions{})
			return err
		},
		func() error { _, err := core.LimitRanges(ns).Create(ctx, o.Limits, metav1.CreateOptions{}); return err },
		func() error {
			_, err := c.Client.NetworkingV1().NetworkPolicies(ns).Create(ctx, o.Network, metav1.CreateOptions{})
			return err
		},
		func() error { _, err := core.Pods(ns).Create(ctx, o.Pod, metav1.CreateOptions{}); return err }, // last: isolated from its first packet
	}
	for _, step := range create {
		if err := step(); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating the lab namespace: %w", err)
		}
	}
	if err := c.waitReady(ctx, ns); err != nil {
		return err
	}
	untar := &ap.Capped{}
	if err := c.run(ctx, ns, []string{"tar", "xzf", "-", "-C", "/lab"}, bytes.NewReader(bundle), untar); err != nil {
		return fmt.Errorf("unpacking the lab: %w: %s", err, tailOf(untar.Bytes()))
	}
	up := &ap.Capped{}
	if err := c.run(ctx, ns, []string{"docker", "compose", "up", "-d", "--wait"}, nil, up); err != nil {
		return fmt.Errorf("docker compose up: %w: %s", err, tailOf(up.Bytes()))
	}
	return nil
}

// waitReady polls the lab pod until dockerd answers (readiness probe), failing fast on states that won't heal.
func (c *ClusterRunner) waitReady(ctx context.Context, ns string) error {
	poll := c.Poll
	if poll == 0 {
		poll = 2 * time.Second
	}
	var unschedulableSince time.Time
	for {
		p, err := c.Client.CoreV1().Pods(ns).Get(ctx, labPod, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("reading the lab pod: %w", err)
		}
		if p.Status.Phase == corev1.PodFailed {
			return fmt.Errorf("the lab pod stopped: %s %s", p.Status.Reason, p.Status.Message)
		}
		for _, cs := range p.Status.ContainerStatuses {
			if w := cs.State.Waiting; w != nil {
				switch w.Reason {
				case "ImagePullBackOff", "ErrImageNeverPull", "InvalidImageName", "CreateContainerConfigError", "CreateContainerError":
					return fmt.Errorf("the lab image could not be started (%s): %s", w.Reason, w.Message)
				}
			}
		}
		for _, cond := range p.Status.Conditions {
			switch {
			case cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue:
				return nil
			case cond.Type == corev1.PodScheduled && cond.Status == corev1.ConditionFalse && cond.Reason == corev1.PodReasonUnschedulable:
				if unschedulableSince.IsZero() {
					unschedulableSince = time.Now()
				}
				if time.Since(unschedulableSince) >= unschedulableGrace {
					return errClusterFull
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
}

// Destroy deletes the lab namespace; Kubernetes removes everything in it. A missing namespace is success, and so is
// one already terminating (the API answers Conflict while it drains).
func (c *ClusterRunner) Destroy(ctx context.Context, inst *Instance) error {
	if !validLabID(inst.ID) {
		return errors.New("invalid lab id")
	}
	err := c.Client.CoreV1().Namespaces().Delete(ctx, labNamespace(inst.ID),
		metav1.DeleteOptions{PropagationPolicy: ptr.To(metav1.DeletePropagationBackground)})
	if apierrors.IsNotFound(err) || apierrors.IsConflict(err) {
		return nil
	}
	return err
}

// Live lists the ids of labs that still have a namespace that is not already being deleted.
func (c *ClusterRunner) Live(ctx context.Context) ([]string, error) {
	list, err := c.Client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{LabelSelector: labLabel})
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for _, ns := range list.Items {
		id := ns.Labels[labLabel]
		if ns.DeletionTimestamp == nil && validLabID(id) && ns.Name == labNamespace(id) {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (c *ClusterRunner) execFn() execFunc {
	if c.exec != nil {
		return c.exec
	}
	return c.realExec
}

// run execs cmd in the lab's dind container without a TTY; stdout and stderr both go to out.
func (c *ClusterRunner) run(ctx context.Context, ns string, cmd []string, stdin io.Reader, out io.Writer) error {
	return c.execFn()(ctx, ns, labPod, cmd, remotecommand.StreamOptions{Stdin: stdin, Stdout: out, Stderr: out})
}

// realExec is `kubectl exec`: WebSocket first, SPDY for older API servers or proxies that refuse the upgrade.
func (c *ClusterRunner) realExec(ctx context.Context, ns, pod string, cmd []string, o remotecommand.StreamOptions) error {
	req := c.Client.CoreV1().RESTClient().Post().Resource("pods").Namespace(ns).Name(pod).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{Container: labContainer, Command: cmd, Stdin: o.Stdin != nil,
			Stdout: o.Stdout != nil, Stderr: o.Stderr != nil, TTY: o.Tty}, scheme.ParameterCodec)
	ws, err := remotecommand.NewWebSocketExecutor(c.Config, "GET", req.URL().String())
	if err != nil {
		return err
	}
	spdy, err := remotecommand.NewSPDYExecutor(c.Config, "POST", req.URL())
	if err != nil {
		return err
	}
	ex, err := remotecommand.NewFallbackExecutor(ws, spdy, func(err error) bool {
		return httpstream.IsUpgradeFailure(err) || httpstream.IsHTTPSProxyError(err)
	})
	if err != nil {
		return err
	}
	return ex.StreamWithContext(ctx, o)
}

// tailOf keeps the end of a command's output for an error message the trainee sees.
func tailOf(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 2000 {
		s = "…" + s[len(s)-2000:]
	}
	return s
}
