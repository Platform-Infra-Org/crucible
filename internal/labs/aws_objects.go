package labs

import (
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

const (
	DefaultTerraformImage = "hashicorp/terraform:1.16.5@sha256:c7926feace05d0f7e73542842bf3945924e955a1f782cf000ccbb8d18fa42d77" // tag 1.16.5 (multi-arch index); bump deliberately

	awsSecret    = "aws-creds" // the lab's STS credentials file; mounted at /aws in the workspace and runner pods
	tfConfigMap  = "tf-module" // module.tgz (terraform/ + crucible.tf + tfvars) and backend.hcl
	tfApplyPod   = "tf-apply"
	tfDestroyPod = "tf-destroy"
	tfVarsFile   = "/w/crucible.auto.tfvars.json" // content.LabTFVarsFile, unpacked from module.tgz

	estimatesNS    = "crucible-estimates" // where estimates' plan pods run
	estimatesLabel = "estimates"          // its crucible.io/lab value: never a lab id, so never taken for a lab
	planDeadline   = 10 * time.Minute     // a plan pod's; its credentials last 15
)

var (
	runnerRequests = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("250m"), corev1.ResourceMemory: resource.MustParse("256Mi")}
	// the aws provider plugin alone unpacks to ~0.6 GiB
	runnerLimits = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("1Gi"),
		corev1.ResourceEphemeralStorage: resource.MustParse("3Gi")}
)

// awsWorkspace turns a cluster lab into an aws lab's workspace (spec §8.2): the dind pod mounts the credentials
// secret at /aws (the workspace service bind-mounts it), and the quota leaves room for one terraform runner pod.
func awsWorkspace(o labObjects) labObjects {
	c := &o.Pod.Spec.Containers[0]
	c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{Name: "aws", MountPath: "/aws", ReadOnly: true})
	o.Pod.Spec.Volumes = append(o.Pod.Spec.Volumes, corev1.Volume{Name: "aws",
		VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: awsSecret}}})
	h := o.Quota.Spec.Hard
	h[corev1.ResourcePods] = resource.MustParse("2")
	add := func(k corev1.ResourceName, v resource.Quantity) {
		q := h[k].DeepCopy()
		q.Add(v)
		h[k] = q
	}
	add(corev1.ResourceRequestsCPU, runnerRequests[corev1.ResourceCPU])
	add(corev1.ResourceRequestsMemory, runnerRequests[corev1.ResourceMemory])
	add(corev1.ResourceLimitsCPU, runnerLimits[corev1.ResourceCPU])
	add(corev1.ResourceLimitsMemory, runnerLimits[corev1.ResourceMemory])
	add(corev1.ResourceLimitsEphemeralStorage, runnerLimits[corev1.ResourceEphemeralStorage])
	return o
}

// tfPod is one terraform run (apply or destroy) in the lab namespace. The module is hostile code (data "external",
// local-exec, file()), so the pod gets nothing beyond the lab's own STS credentials: no service-account token, no
// service env, non-root on a read-only root with no capabilities, and the lab's NetworkPolicy (DNS and the public
// internet only: IMDS, the node, the Kubernetes API, Postgres and other labs are blocked). It is a bare pod rather
// than a batch/v1 Job: one attempt is what we want, a fixed name lets a retry find it, and FallbackToLogsOnError
// puts the log tail in the pod status, which `pods get` (already granted) reads.
func tfPod(id, name, image, script string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: labNamespace(id), Labels: map[string]string{labLabel: id}},
		Spec: corev1.PodSpec{
			RestartPolicy:                 corev1.RestartPolicyNever,
			AutomountServiceAccountToken:  ptr.To(false),
			EnableServiceLinks:            ptr.To(false),
			ActiveDeadlineSeconds:         ptr.To(int64(50 * 60)), // inside the one-hour credentials
			TerminationGracePeriodSeconds: ptr.To(int64(120)),     // terraform stops cleanly on SIGTERM
			SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: ptr.To(true), RunAsUser: ptr.To(int64(65532)),
				RunAsGroup: ptr.To(int64(65532)), FSGroup: ptr.To(int64(65532)),
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
			Containers: []corev1.Container{{
				Name:       "terraform",
				Image:      image,
				Command:    []string{"/bin/sh", "-c", script},
				WorkingDir: "/w",
				Env: []corev1.EnvVar{
					{Name: "AWS_SHARED_CREDENTIALS_FILE", Value: "/aws/credentials"},
					{Name: "AWS_EC2_METADATA_DISABLED", Value: "true"}, // belt to the NetworkPolicy's IMDS block
					{Name: "HOME", Value: "/tmp"},
					{Name: "TF_IN_AUTOMATION", Value: "1"},
					{Name: "TF_INPUT", Value: "0"},
					{Name: "CHECKPOINT_DISABLE", Value: "1"}, // no phone-home to checkpoint-api.hashicorp.com
				},
				Resources:                corev1.ResourceRequirements{Requests: runnerRequests, Limits: runnerLimits},
				TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
				SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: ptr.To(false), ReadOnlyRootFilesystem: ptr.To(true),
					Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
				VolumeMounts: []corev1.VolumeMount{
					{Name: "aws", MountPath: "/aws", ReadOnly: true},
					{Name: "module", MountPath: "/module", ReadOnly: true},
					{Name: "work", MountPath: "/w"},
					{Name: "tmp", MountPath: "/tmp"},
				},
			}},
			Volumes: []corev1.Volume{
				{Name: "aws", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: awsSecret}}},
				{Name: "module", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: tfConfigMap}}}},
				{Name: "work", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
				{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
			},
		},
	}
}

// imdsGuard holds a runner pod back until IMDS is unreachable from it. A NetworkPolicy engine enforces a new pod's
// policy only once it has seen the pod's IP: make cluster-check measured a fresh pod on kind reaching 169.254.169.254
// in its first second, and the node's IMDS (hop limit 2) hands out the node role, which can assume lab roles. It fails
// closed after about a minute. ponytail: one probe of IMDS on port 80; the policy blocks the whole range anyway.
const imdsGuard = "n=0; while echo | nc -w 2 169.254.169.254 80 >/dev/null 2>&1; do n=$((n+1)); " +
	"[ $n -lt 30 ] || { echo 'IMDS is reachable: the lab NetworkPolicy is not in force'; exit 1; }; sleep 1; done\n"

// tfScript is the runner pod's shell script. Crucible's variables go on the command line (-var-file there has the
// highest precedence, so no author *.auto.tfvars can override the lab id the tags and IAM conditions rely on). init
// runs in an empty directory with no -force-copy, so there is no local state to migrate. Destroy runs only after
// the apply pod is gone, so -lock=false cannot race an apply, and a lock left by a killed apply cannot wedge it.
// A dry run (CRUCIBLE_AWS_LABS=dryrun) proves the pod, its mounts and the module unpack, and touches no backend
// and no cloud.
func tfScript(action string, dryRun bool) string {
	s := "set -eu\n" + imdsGuard + "tar xzf /module/module.tgz -C /w\n"
	if dryRun {
		return s + "terraform version\nls /w\necho 'dry run (CRUCIBLE_AWS_LABS=dryrun): nothing was applied or destroyed'\n"
	}
	s += "terraform init -input=false -no-color -backend-config=/module/backend.hcl\n"
	if action == "destroy" {
		return s + "terraform destroy -input=false -no-color -auto-approve -lock=false -var-file=" + tfVarsFile + "\n"
	}
	return s + "terraform apply -input=false -no-color -auto-approve -var-file=" + tfVarsFile + "\n"
}

// estimatesObjects are the namespace estimates run in and its NetworkPolicy: a lab namespace's (Pod Security
// baseline, DNS and the public internet only), created on first use.
func estimatesObjects() (*corev1.Namespace, *networkingv1.NetworkPolicy) {
	o := clusterObjects(estimatesLabel, "", false)
	o.Namespace.Name, o.Network.Namespace = estimatesNS, estimatesNS
	return o.Namespace, o.Network
}

// planPod is one estimate's terraform plan: an apply pod's lockdown, in crucible-estimates, mounting its own run's
// credentials secret and module config map (both named like the pod).
func planPod(name, image string) *corev1.Pod {
	p := tfPod(estimatesLabel, name, image, planScript)
	p.Namespace = estimatesNS
	p.Spec.ActiveDeadlineSeconds = ptr.To(int64(planDeadline.Seconds()))
	for i := range p.Spec.Volumes {
		if v := &p.Spec.Volumes[i]; v.Secret != nil {
			v.Secret.SecretName = name
		} else if v.ConfigMap != nil {
			v.ConfigMap.Name = name
		}
	}
	return p
}

// planScript plans the module against no backend (planTF), no refresh and no lock, so it reads and writes no state.
// Data sources still read the account, with the plan role. Everything but the plan JSON goes to stderr; the pod log
// mixes both streams, so the JSON is its last line, printed by terraform itself after the module's code has run.
const planScript = "set -eu\n" + imdsGuard + "tar xzf /module/module.tgz -C /w\n" +
	"terraform init -input=false -no-color -backend=false >&2\n" +
	"terraform plan -input=false -no-color -refresh=false -lock=false -var-file=" + tfVarsFile + " -out=/w/p >&2\n" +
	"terraform show -no-color -json /w/p\n"
