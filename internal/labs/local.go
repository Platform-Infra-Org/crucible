package labs

import (
	"context"
	"strings"

	"crucible/internal/agenthub"
	ap "crucible/internal/agentproto"
	"crucible/internal/apperr"
)

// LocalRunner runs labs on the trainee's laptop through crucible-agent.
type LocalRunner struct{ Hub *agenthub.Hub }

var errAgentOffline = apperr.Wrap(apperr.Unavailable, "your laptop agent is not connected. Open “Connect your laptop”")

func (l LocalRunner) Available(inst *Instance) error {
	if !l.Hub.Online(inst.UserID) {
		return errAgentOffline
	}
	return nil
}

func (l LocalRunner) Provision(ctx context.Context, inst *Instance, bundle []byte, compose string) error {
	_, err := l.Hub.Call(ctx, inst.UserID, ap.Msg{Type: ap.TProvision, LabID: inst.ID, Data: bundle, Compose: compose})
	return err
}

func (l LocalRunner) Destroy(ctx context.Context, inst *Instance) error {
	_, err := l.Hub.Call(ctx, inst.UserID, ap.Msg{Type: ap.TDestroy, LabID: inst.ID})
	return err
}

func (l LocalRunner) RunScript(ctx context.Context, inst *Instance, s ScriptSpec) (ScriptResult, error) {
	res, err := l.Hub.Call(ctx, inst.UserID, ap.Msg{Type: ap.TRunScript, LabID: inst.ID, Service: s.Service,
		Data: s.Script, Env: s.Env, TimeoutMS: s.Timeout.Milliseconds()})
	if err != nil {
		return ScriptResult{}, err
	}
	return ScriptResult{ExitCode: res.ExitCode, Output: cleanText(string(res.Data)), TimedOut: res.TimedOut}, nil
}

// cleanText makes arbitrary bytes storable in Postgres TEXT: no NUL, valid UTF-8 (a rune cut by the output cap becomes U+FFFD).
func cleanText(s string) string {
	return strings.ToValidUTF8(strings.ReplaceAll(s, "\x00", ""), "\uFFFD")
}

func (l LocalRunner) OpenPTY(ctx context.Context, inst *Instance, service string, cols, rows int) (PTY, error) {
	p, err := l.Hub.OpenPTY(ctx, inst.UserID, inst.ID, service, cols, rows)
	if err != nil {
		return nil, err // avoid a typed-nil *agenthub.PTY inside the interface
	}
	return p, nil
}
