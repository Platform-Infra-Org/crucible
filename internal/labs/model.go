// Package labs runs lab instances: lifecycle, tasks, checks, setup scripts, hints, timers and idle detection.
package labs

import (
	"context"
	"io"
	"time"

	"crucible/internal/config"
	"crucible/internal/content"
)

type State string

const (
	Provisioning State = "provisioning"
	Ready        State = "ready"
	Destroying   State = "destroying"
	Destroyed    State = "destroyed"
	Failed       State = "failed"
)

type Instance struct {
	ID                                          string
	UserID                                      int64
	Team, Training, Module, SHA, Runtime        string
	State                                       State
	Error                                       string
	CreatedAt                                   time.Time
	ReadyAt, EndsAt                             *time.Time
	LimitReason, EndReason                      string
	LastActivityAt                              time.Time
	TTL, IdleTimeout, IdleWarning, MaxExtension time.Duration
	Extended                                    bool
}

// Limit is one candidate end time for a lab (TTL now; schedule window and budget cap arrive in M3/M6).
type Limit struct {
	At     time.Time
	Reason string
}

func EffectiveEnd(limits ...Limit) Limit {
	var best Limit
	for _, l := range limits {
		if l.At.IsZero() {
			continue
		}
		if best.At.IsZero() || l.At.Before(best.At) {
			best = l
		}
	}
	return best
}

type Timing struct{ TTL, IdleTimeout, IdleWarning, MaxExtension time.Duration }

func ResolveTiming(lab *content.Lab, d config.LabDefaults) Timing {
	t := Timing{TTL: 2 * time.Hour, IdleTimeout: 30 * time.Minute, IdleWarning: lab.IdleWarning.D(), MaxExtension: d.MaxExtension.D()}
	if d.TTL > 0 {
		t.TTL = d.TTL.D()
	}
	if lab.TTL > 0 {
		t.TTL = lab.TTL.D()
	}
	if d.IdleTimeout > 0 {
		t.IdleTimeout = d.IdleTimeout.D()
	}
	if lab.IdleTimeout > 0 {
		t.IdleTimeout = lab.IdleTimeout.D()
	}
	if t.IdleWarning <= 0 || t.IdleWarning >= t.IdleTimeout {
		t.IdleWarning = t.IdleTimeout / 2
	}
	return t
}

type ScriptSpec struct {
	Service string
	Script  []byte
	Env     map[string]string
	Timeout time.Duration
}

type ScriptResult struct {
	ExitCode int
	Output   string
	TimedOut bool
}

type PTY interface {
	io.ReadWriteCloser
	Resize(cols, rows int) error
}

// Runner hosts lab environments for one runtime (local now; cluster in M4, aws in M6).
type Runner interface {
	Available(inst *Instance) error
	Provision(ctx context.Context, inst *Instance, bundle []byte, compose string) error
	OpenPTY(ctx context.Context, inst *Instance, service string, cols, rows int) (PTY, error)
	RunScript(ctx context.Context, inst *Instance, s ScriptSpec) (ScriptResult, error)
	Destroy(ctx context.Context, inst *Instance) error
}
