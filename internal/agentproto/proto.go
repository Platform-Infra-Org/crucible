// Package agentproto is the JSON message format between crucible-api and crucible-agent.
package agentproto

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
}
