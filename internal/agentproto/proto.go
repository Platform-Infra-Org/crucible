// Package agentproto is the JSON message format between crucible-api and crucible-agent.
package agentproto

import (
	"bytes"
	"sync"
)

const (
	TProvision = "provision"  // API → agent: Data = lab bundle (tar.gz), Compose = compose file name
	TDestroy   = "destroy"    // API → agent
	TRunScript = "run_script" // API → agent: Service, Data = script, Env, TimeoutMS
	TPTYOpen   = "pty_open"   // API → agent: ID = PTY session id, Service, Cols, Rows
	TPTYData   = "pty_data"   // both ways: ID = PTY session id
	TPTYResize = "pty_resize" // API → agent
	TPTYClose  = "pty_close"  // both ways
	TResult    = "result"     // agent → API: answer to a request with the same ID
	THello     = "hello"      // agent → API right after connecting: Data = JSON array of lab IDs present on the laptop
	TContexts  = "contexts"   // agent → API whenever the kubeconfig's contexts change: Contexts
)

// MaxOutput caps script output kept and sent back (spec: 64 KiB).
const MaxOutput = 64 << 10

type Msg struct {
	Type      string            `json:"type"`
	ID        string            `json:"id,omitempty"`
	LabID     string            `json:"lab_id,omitempty"`
	Service   string            `json:"service,omitempty"`
	Compose   string            `json:"compose,omitempty"`
	Data      []byte            `json:"data,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	TimeoutMS int64             `json:"timeout_ms,omitempty"`
	Cols      int               `json:"cols,omitempty"`
	Rows      int               `json:"rows,omitempty"`
	ExitCode  int               `json:"exit_code"`
	TimedOut  bool              `json:"timed_out,omitempty"`
	Error     string            `json:"error,omitempty"`
	Cluster   *ClusterSpec      `json:"cluster,omitempty"`  // provision of a cluster lab
	Contexts  []KubeContext     `json:"contexts,omitempty"` // hello and contexts: the agent's kubeconfig contexts
}

// Capped keeps the first MaxOutput bytes written to it and silently drops the rest. Safe for concurrent writers
// (stdout and stderr of one script).
type Capped struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *Capped) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if room := MaxOutput - c.buf.Len(); room > 0 {
		c.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

func (c *Capped) Bytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return bytes.Clone(c.buf.Bytes())
}

// KubeContext is one context of the agent's kubeconfig: its name and its API server's host. Never credentials.
type KubeContext struct {
	Name   string `json:"name"`
	Server string `json:"server"`
}

// Chart is one Helm chart a cluster lab installs, in order.
type Chart struct {
	Name     string `json:"name"`             // the release name
	Path     string `json:"path"`             // a folder in the lab bundle
	Values   string `json:"values,omitempty"` // a file in the lab bundle
	TimeoutS int64  `json:"timeout_s"`        // helm --wait --timeout
}

// ClusterSpec is what a cluster lab's provision carries besides its bundle.
type ClusterSpec struct {
	Context     string  `json:"context"`
	Charts      []Chart `json:"charts"`
	ClusterWide bool    `json:"cluster_wide"`
	Toolbox     string  `json:"toolbox"` // the toolbox image the server pins
}

// ClusterScopedKinds are the kinds a chart may create only when its lab declares cluster_wide.
var ClusterScopedKinds = map[string]bool{"CustomResourceDefinition": true, "ClusterRole": true, "ClusterRoleBinding": true,
	"Namespace": true, "StorageClass": true, "PriorityClass": true, "MutatingWebhookConfiguration": true,
	"ValidatingWebhookConfiguration": true, "PersistentVolume": true, "IngressClass": true, "APIService": true}
