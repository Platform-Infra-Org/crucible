package labs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"crucible/internal/apperr"
	"crucible/internal/content"
	"crucible/internal/infracost"
)

const (
	estimateSlots   = 2               // infracost runs at once, across all labs
	estimateFailTTL = 5 * time.Minute // how long a failed run is remembered
)

// InfracostEstimator prices aws labs (spec §9.1). Trainee lobby views call it, so runs are bounded: results are
// cached per content version (lab.Dir is an immutable export, so the CLI runs once per lab per pushed commit),
// concurrent views of one version share one run, failures are remembered for estimateFailTTL, and at most
// estimateSlots runs go at once; a view that finds no free slot is told to retry rather than queueing. A run is
// detached from the request that started it (the CLI has its own timeout, infracost.Exec), so a closed tab does not
// waste it.
// ponytail: the caches never shrink (one entry per lab version).
type InfracostEstimator struct {
	Run infracost.Runner
	Now func() time.Time // nil = time.Now

	mu       sync.Mutex
	cache    map[string]float64
	failed   map[string]estimateFailure
	inflight map[string]*estimateCall
	running  int
}

type estimateFailure struct {
	err   error
	until time.Time
}

type estimateCall struct {
	done chan struct{}
	h    float64
	err  error
}

func (e *InfracostEstimator) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *InfracostEstimator) HourlyUSD(ctx context.Context, lab *content.Lab) (float64, error) {
	if lab.AWS == nil {
		return 0, errors.New("not an aws lab")
	}
	key := lab.Dir
	e.mu.Lock()
	if h, ok := e.cache[key]; ok {
		e.mu.Unlock()
		return h, nil
	}
	if f, ok := e.failed[key]; ok && e.now().Before(f.until) {
		e.mu.Unlock()
		return 0, f.err
	}
	c := e.inflight[key]
	if c == nil {
		if e.running >= estimateSlots {
			e.mu.Unlock()
			return 0, apperr.Wrap(apperr.Unavailable, "estimate pending, try again shortly")
		}
		if e.inflight == nil {
			e.cache, e.failed, e.inflight = map[string]float64{}, map[string]estimateFailure{}, map[string]*estimateCall{}
		}
		c = &estimateCall{done: make(chan struct{})}
		e.inflight[key] = c
		e.running++
		go e.run(context.WithoutCancel(ctx), key, lab.AWS.Region, c)
	}
	e.mu.Unlock()
	select {
	case <-c.done:
		return c.h, c.err
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

func (e *InfracostEstimator) run(ctx context.Context, key, region string, c *estimateCall) {
	defer func() { // a panic here would otherwise kill the API (no request handler's recover covers this goroutine)
		if r := recover(); r != nil {
			slog.Error("infracost estimate panicked", "lab", key, "panic", r)
			c.h, c.err = 0, fmt.Errorf("estimate failed: %v", r)
		}
		e.finish(key, c)
	}()
	c.h, c.err = infracost.Hourly(ctx, e.Run, filepath.Join(key, "terraform"), region)
}

// finish records c's outcome, frees its slot and wakes its waiters.
func (e *InfracostEstimator) finish(key string, c *estimateCall) {
	e.mu.Lock()
	if c.err != nil {
		e.failed[key] = estimateFailure{c.err, e.now().Add(estimateFailTTL)}
	} else {
		e.cache[key] = c.h
	}
	delete(e.inflight, key)
	e.running--
	e.mu.Unlock()
	close(c.done)
}
