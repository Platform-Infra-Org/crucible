// Package labs runs lab instances: lifecycle, tasks, checks, setup scripts, hints, timers and idle detection.
package labs

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"crucible/internal/apperr"
	"crucible/internal/config"
	"crucible/internal/content"
	"crucible/internal/gitsync"
)

type State string

const (
	PendingApproval State = "pending_approval"
	Provisioning    State = "provisioning"
	Ready           State = "ready"
	Destroying      State = "destroying"
	Destroyed       State = "destroyed"
	Failed          State = "failed"
	Rejected        State = "rejected"
	Expired         State = "expired" // unanswered at every tier, or withdrawn by the trainee
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
	HourlyUSD, EstimateUSD                      float64
	Tier                                        string
	OverCap                                     bool
	EscalateAt, DecidedAt                       *time.Time
	DecidedBy, DecisionNote                     string
	ExtUntil, ExtRequestedAt                    *time.Time // a pending extension (spec §8.6); nil = none
	ExtEstimateUSD                              float64
	ExtTier                                     string
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

// Estimator prices one hour of a lab for approval routing and budgets (spec §9.1). local labs are free; cluster
// (M4: internal rate card from platform.yaml) and aws (M6: infracost on the module) plug in here per runtime.
type Estimator interface {
	HourlyUSD(ctx context.Context, lab *content.Lab) (float64, error)
}

// FixedRates prices labs by lab id; unlisted labs are free. Production uses it empty for local labs; the local
// e2e check sets CRUCIBLE_DEV_LAB_USD_PER_HOUR so a laptop lab exercises the approval path.
type FixedRates map[string]float64

func (f FixedRates) HourlyUSD(_ context.Context, lab *content.Lab) (float64, error) {
	return f[lab.ID], nil
}

// PlatformRate prices cluster labs from platform.yaml cluster_usd_per_hour (one rate: the lab size is fixed).
// Fail closed: no platform or no rate configured is Unavailable, never $0. Override (the dev env, by lab id) wins.
type PlatformRate struct {
	State    func() *gitsync.State
	Override FixedRates
}

func (r PlatformRate) HourlyUSD(_ context.Context, lab *content.Lab) (float64, error) {
	if v, ok := r.Override[lab.ID]; ok {
		return v, nil
	}
	var st *gitsync.State
	if r.State != nil {
		st = r.State()
	}
	if st == nil || st.Platform == nil || st.Platform.Settings.ClusterUSDPerHour == nil {
		return 0, apperr.Wrap(apperr.Unavailable, "cluster labs have no price: set cluster_usd_per_hour in platform.yaml")
	}
	return *st.Platform.Settings.ClusterUSDPerHour, nil
}

// ParseRates reads "lab-id=0.5,other=1".
func ParseRates(s string) (FixedRates, error) {
	out := FixedRates{}
	for _, kv := range strings.Split(s, ",") {
		if kv = strings.TrimSpace(kv); kv == "" {
			continue
		}
		id, v, ok := strings.Cut(kv, "=")
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if !ok || err != nil || f < 0 || strings.TrimSpace(id) == "" {
			return nil, fmt.Errorf("bad lab rate %q (want lab-id=usd-per-hour)", kv)
		}
		out[strings.TrimSpace(id)] = f
	}
	return out, nil
}
