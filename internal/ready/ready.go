// Package ready judges whether a Helm release's resources are ready, the way kubectl rollout status would, once and
// without waiting. crucible-ready runs it in a cluster lab's toolbox; the server shows its Report as release health.
package ready

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type Status string

const (
	Healthy     Status = "healthy"
	Progressing Status = "progressing"
	Failing     Status = "failing"
)

type Release struct {
	Name    string   `json:"name"`
	Status  Status   `json:"status"`
	Unready []string `json:"unready"`
}

type Report struct {
	Releases []Release `json:"releases"`
}

type Object struct{ Kind, Name string }

// Parse reads the kinds and names of a release manifest (helm get manifest).
func Parse(manifest []byte) []Object {
	var out []Object
	dec := yaml.NewDecoder(bytes.NewReader(manifest))
	for {
		var d struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Name string `yaml:"name"`
			} `yaml:"metadata"`
		}
		if err := dec.Decode(&d); err != nil {
			return out
		}
		if d.Kind != "" && d.Metadata.Name != "" {
			out = append(out, Object{d.Kind, d.Metadata.Name})
		}
	}
}

// Judge checks each object; kinds without a notion of readiness count as ready.
func Judge(ctx context.Context, cs kubernetes.Interface, ns, release string, objs []Object) Release {
	r := Release{Name: release, Status: Healthy, Unready: []string{}}
	for _, o := range objs {
		msg, failed := judge(ctx, cs, ns, o)
		if msg == "" {
			continue
		}
		r.Unready = append(r.Unready, strings.ToLower(o.Kind)+"/"+o.Name+": "+msg)
		if failed {
			r.Status = Failing
		} else if r.Status == Healthy {
			r.Status = Progressing
		}
	}
	return r
}

func judge(ctx context.Context, cs kubernetes.Interface, ns string, o Object) (msg string, failed bool) {
	get := metav1.GetOptions{}
	notFound := func(err error) (string, bool) {
		if apierrors.IsNotFound(err) {
			return "not found", false
		}
		return err.Error(), false
	}
	switch o.Kind {
	case "Deployment":
		d, err := cs.AppsV1().Deployments(ns).Get(ctx, o.Name, get)
		if err != nil {
			return notFound(err)
		}
		want := int32(1)
		if d.Spec.Replicas != nil {
			want = *d.Spec.Replicas
		}
		if d.Status.ObservedGeneration < d.Generation || d.Status.UpdatedReplicas < want || d.Status.AvailableReplicas < want {
			return rollout(d.Status.UpdatedReplicas, d.Status.AvailableReplicas, want), false
		}
	case "StatefulSet":
		s, err := cs.AppsV1().StatefulSets(ns).Get(ctx, o.Name, get)
		if err != nil {
			return notFound(err)
		}
		want := int32(1)
		if s.Spec.Replicas != nil {
			want = *s.Spec.Replicas
		}
		if s.Status.ReadyReplicas < want || s.Status.UpdatedReplicas < want {
			return rollout(s.Status.UpdatedReplicas, s.Status.ReadyReplicas, want), false
		}
	case "DaemonSet":
		d, err := cs.AppsV1().DaemonSets(ns).Get(ctx, o.Name, get)
		if err != nil {
			return notFound(err)
		}
		if d.Status.NumberReady < d.Status.DesiredNumberScheduled || d.Status.UpdatedNumberScheduled < d.Status.DesiredNumberScheduled {
			return rollout(d.Status.UpdatedNumberScheduled, d.Status.NumberReady, d.Status.DesiredNumberScheduled), false
		}
	case "Job":
		j, err := cs.BatchV1().Jobs(ns).Get(ctx, o.Name, get)
		if err != nil {
			return notFound(err)
		}
		for _, c := range j.Status.Conditions {
			if c.Status == corev1.ConditionTrue && c.Type == batchv1.JobFailed {
				return "failed", true
			}
			if c.Status == corev1.ConditionTrue && c.Type == batchv1.JobComplete {
				return "", false
			}
		}
		return "running", false
	case "Pod":
		p, err := cs.CoreV1().Pods(ns).Get(ctx, o.Name, get)
		if err != nil {
			return notFound(err)
		}
		if p.Status.Phase == corev1.PodFailed {
			return "failed", true
		}
		for _, c := range p.Status.Conditions {
			if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
				return "", false
			}
		}
		return string(p.Status.Phase), false
	case "PersistentVolumeClaim":
		c, err := cs.CoreV1().PersistentVolumeClaims(ns).Get(ctx, o.Name, get)
		if err != nil {
			return notFound(err)
		}
		if c.Status.Phase != corev1.ClaimBound {
			return string(c.Status.Phase), false
		}
	case "Service":
		s, err := cs.CoreV1().Services(ns).Get(ctx, o.Name, get)
		if err != nil {
			return notFound(err)
		}
		if s.Spec.Type == corev1.ServiceTypeExternalName || len(s.Spec.Selector) == 0 {
			return "", false
		}
		slices, err := cs.DiscoveryV1().EndpointSlices(ns).List(ctx, metav1.ListOptions{LabelSelector: "kubernetes.io/service-name=" + o.Name})
		if err != nil {
			return err.Error(), false
		}
		for _, sl := range slices.Items {
			for _, e := range sl.Endpoints {
				if e.Conditions.Ready != nil && *e.Conditions.Ready {
					return "", false
				}
			}
		}
		return "no ready endpoints", false
	}
	return "", false
}

// rollout names what holds a workload back: replicas not yet updated (a stuck rollout keeps the old ones ready), else
// replicas not yet ready.
func rollout(updated, ready, want int32) string {
	if updated < want {
		return fmt.Sprintf("%d of %d updated", updated, want)
	}
	return fmt.Sprintf("%d of %d ready", ready, want)
}
