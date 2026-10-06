package labs

import (
	"regexp"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
)

const (
	labImage     = "docker:28-dind@sha256:2a232a42256f70d78e3cc5d2b5d6b3276710a0de0596c145f627ecfae90282ac" // dockerd + the compose plugin (multi-arch index); bump deliberately
	labPod       = "lab"
	labContainer = "dind"
	labLabel     = "crucible.io/lab"
	sysboxClass  = "sysbox-runc"
)

// ponytail: one size for every lab (requests 500m/1Gi/10Gi: about 6 labs fit a t3a.xlarge by request after the platform pods' share, but 4Gi limits make 3 the real memory-safe number); add a lab.yaml
// resources block when a lab needs more.
var (
	labLimits = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2"), corev1.ResourceMemory: resource.MustParse("4Gi"),
		corev1.ResourceEphemeralStorage: resource.MustParse("21Gi")}
	labRequests = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("1Gi"),
		corev1.ResourceEphemeralStorage: resource.MustParse("10Gi")}
	dockerDisk = resource.MustParse("20Gi")
)

// blockedEgress can never be reached from a lab: link-local (cloud metadata, IMDS at 169.254.169.254) and every
// private range, which covers the node, the Kubernetes API, Postgres, crucible-api and other labs. There is no IPv6
// allow rule, so IPv6 (including the IPv6 IMDS endpoint) is denied as well.
var blockedEgress = []string{"169.254.0.0/16", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10"}

var labIDRe = regexp.MustCompile(`^[0-9a-f]{12}$`)

// validLabID guards every Kubernetes call: a lab id becomes part of a namespace name.
func validLabID(id string) bool { return labIDRe.MatchString(id) }

func labNamespace(id string) string { return "lab-" + id }

var guardResources = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m"), corev1.ResourceMemory: resource.MustParse("32Mi"),
	corev1.ResourceEphemeralStorage: resource.MustParse("16Mi")}

// imdsGuardContainer holds dind back until IMDS is unreachable, i.e. the lab NetworkPolicy is in force (the same
// guard as the terraform pods; it adds a few seconds to a lab start). It runs the dind image's busybox nc, which the
// node already has, as a non-root user with nothing mounted.
func imdsGuardContainer() corev1.Container {
	return corev1.Container{
		Name:                     "imds-guard",
		Image:                    labImage,
		Command:                  []string{"/bin/sh", "-c", imdsGuard},
		Resources:                corev1.ResourceRequirements{Requests: guardResources, Limits: guardResources},
		TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
		SecurityContext: &corev1.SecurityContext{RunAsNonRoot: ptr.To(true), RunAsUser: ptr.To(int64(65532)), RunAsGroup: ptr.To(int64(65532)),
			AllowPrivilegeEscalation: ptr.To(false), ReadOnlyRootFilesystem: ptr.To(true),
			Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
	}
}

type labObjects struct {
	Namespace *corev1.Namespace
	Quota     *corev1.ResourceQuota
	Limits    *corev1.LimitRange
	Network   *networkingv1.NetworkPolicy
	Pod       *corev1.Pod
}

// clusterObjects is everything one cluster lab runs in (spec §8.2). privileged is only for dev clusters without
// sysbox (kind on Docker Desktop): a privileged dind pod and no Pod Security enforcement.
func clusterObjects(id, compose string, privileged bool) labObjects {
	ns := labNamespace(id)
	meta := func(name string) metav1.ObjectMeta {
		return metav1.ObjectMeta{Name: name, Namespace: ns, Labels: map[string]string{labLabel: id}}
	}
	nsLabels := map[string]string{labLabel: id}
	if !privileged {
		nsLabels["pod-security.kubernetes.io/enforce"] = "baseline" // the API server refuses privileged pods here
	}
	pod := &corev1.Pod{ObjectMeta: meta(labPod), Spec: corev1.PodSpec{
		RestartPolicy:                corev1.RestartPolicyNever,
		AutomountServiceAccountToken: ptr.To(false),
		EnableServiceLinks:           ptr.To(false),
		InitContainers:               []corev1.Container{imdsGuardContainer()},
		Containers: []corev1.Container{{
			Name:  labContainer,
			Image: labImage,
			Args:  []string{"dockerd", "--host=unix:///var/run/docker.sock"}, // no TCP socket (the image default adds :2375)
			Env: []corev1.EnvVar{
				{Name: "COMPOSE_PROJECT_NAME", Value: "lab"},
				{Name: "COMPOSE_FILE", Value: "/lab/" + compose}, // every exec inherits it: plain `docker compose …` works
				{Name: "DOCKER_TLS_CERTDIR", Value: ""},
			},
			Resources: corev1.ResourceRequirements{Limits: labLimits, Requests: labRequests},
			ReadinessProbe: &corev1.Probe{
				ProbeHandler:  corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{"docker", "info"}}},
				PeriodSeconds: 2, TimeoutSeconds: 5,
			},
			VolumeMounts: []corev1.VolumeMount{{Name: "docker", MountPath: "/var/lib/docker"}, {Name: "lab", MountPath: "/lab"}},
		}},
		Volumes: []corev1.Volume{
			{Name: "docker", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: ptr.To(dockerDisk)}}},
			{Name: "lab", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		},
	}}
	if privileged {
		pod.Spec.Containers[0].SecurityContext = &corev1.SecurityContext{Privileged: ptr.To(true)}
	} else {
		pod.Spec.RuntimeClassName = ptr.To(sysboxClass)
		pod.Spec.HostUsers = ptr.To(false)
	}
	dns := networkingv1.NetworkPolicyEgressRule{
		To: []networkingv1.NetworkPolicyPeer{{
			NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": "kube-system"}},
			PodSelector:       &metav1.LabelSelector{MatchLabels: map[string]string{"k8s-app": "kube-dns"}},
		}},
		Ports: []networkingv1.NetworkPolicyPort{
			{Protocol: ptr.To(corev1.ProtocolUDP), Port: ptr.To(intstr.FromInt32(53))},
			{Protocol: ptr.To(corev1.ProtocolTCP), Port: ptr.To(intstr.FromInt32(53))},
		},
	}
	internet := networkingv1.NetworkPolicyEgressRule{
		To: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: "0.0.0.0/0", Except: blockedEgress}}},
	}
	return labObjects{
		Namespace: &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns, Labels: nsLabels}},
		Quota: &corev1.ResourceQuota{ObjectMeta: meta("lab-quota"), Spec: corev1.ResourceQuotaSpec{Hard: corev1.ResourceList{
			corev1.ResourcePods:                   resource.MustParse("1"),
			corev1.ResourceRequestsCPU:            labRequests[corev1.ResourceCPU],
			corev1.ResourceRequestsMemory:         labRequests[corev1.ResourceMemory],
			corev1.ResourceLimitsCPU:              labLimits[corev1.ResourceCPU],
			corev1.ResourceLimitsMemory:           labLimits[corev1.ResourceMemory],
			corev1.ResourceLimitsEphemeralStorage: labLimits[corev1.ResourceEphemeralStorage],
		}}},
		Limits: &corev1.LimitRange{ObjectMeta: meta("lab-limits"), Spec: corev1.LimitRangeSpec{Limits: []corev1.LimitRangeItem{{
			Type:           corev1.LimitTypeContainer,
			Max:            corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2"), corev1.ResourceMemory: resource.MustParse("4Gi")},
			Default:        corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("2Gi")},
			DefaultRequest: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("250m"), corev1.ResourceMemory: resource.MustParse("512Mi")},
		}}}},
		Network: &networkingv1.NetworkPolicy{ObjectMeta: meta("lab-default-deny"), Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress},
			Egress:      []networkingv1.NetworkPolicyEgressRule{dns, internet},
		}},
		Pod: pod,
	}
}
