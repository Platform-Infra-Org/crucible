package labs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"crucible/internal/apperr"
	"crucible/internal/content"
	"crucible/internal/pricing"
)

const (
	estimateSlots   = 2               // plans at once, across all labs
	estimateFailTTL = 5 * time.Minute // how long a failed run is remembered
)

// Planner runs a read-only `terraform plan` of an aws lab's module and returns `terraform show -json` of it.
type Planner interface {
	Plan(ctx context.Context, lab *content.Lab) ([]byte, error)
}

// TerraformEstimator prices aws labs (spec 2026-10-12 §2): a read-only plan of the module, priced by Crucible from
// the free AWS Price List, with the lab's declared prices and usage. It fails closed: a failed plan, stale or missing
// prices, a type Crucible can't price, a $0 total or a total over the lab's max_hourly_usd is an error, never a price.
// Trainee lobby views call it, so runs are bounded: results are cached per content version (lab.Dir is an immutable
// export, so the plan runs once per lab per pushed commit), concurrent views of one version share one run, failures
// are remembered for estimateFailTTL, and at most estimateSlots runs go at once; a view that finds no free slot is
// told to retry rather than queueing. A run is detached from the request that started it (the Planner bounds its own
// time), so a closed tab does not waste it.
// ponytail: the caches never shrink (one entry per lab version).
type TerraformEstimator struct {
	Plan   Planner
	Prices func(context.Context) (pricing.Lookup, error)
	Now    func() time.Time // nil = time.Now

	mu       sync.Mutex
	cache    map[string]pricing.Breakdown
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
	b    pricing.Breakdown
	err  error
}

func (e *TerraformEstimator) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *TerraformEstimator) HourlyUSD(ctx context.Context, lab *content.Lab) (float64, error) {
	b, err := e.Breakdown(ctx, lab)
	if err != nil {
		return 0, err
	}
	return b.HourlyUSD, nil
}

// Breakdown is the priced plan, line by line, for the lab's content version (the same run HourlyUSD uses).
func (e *TerraformEstimator) Breakdown(ctx context.Context, lab *content.Lab) (pricing.Breakdown, error) {
	if lab.AWS == nil {
		return pricing.Breakdown{}, errors.New("not an aws lab")
	}
	key := lab.Dir
	e.mu.Lock()
	if b, ok := e.cache[key]; ok {
		e.mu.Unlock()
		return b, nil
	}
	if f, ok := e.failed[key]; ok && e.now().Before(f.until) {
		e.mu.Unlock()
		return pricing.Breakdown{}, f.err
	}
	c := e.inflight[key]
	if c == nil {
		if e.running >= estimateSlots {
			e.mu.Unlock()
			return pricing.Breakdown{}, apperr.Wrap(apperr.Unavailable, "estimate pending, try again shortly")
		}
		if e.inflight == nil {
			e.cache, e.failed, e.inflight = map[string]pricing.Breakdown{}, map[string]estimateFailure{}, map[string]*estimateCall{}
		}
		c = &estimateCall{done: make(chan struct{})}
		e.inflight[key] = c
		e.running++
		go e.run(context.WithoutCancel(ctx), lab, c)
	}
	e.mu.Unlock()
	select {
	case <-c.done:
		return c.b, c.err
	case <-ctx.Done():
		return pricing.Breakdown{}, ctx.Err()
	}
}

func (e *TerraformEstimator) run(ctx context.Context, lab *content.Lab, c *estimateCall) {
	defer func() { // a panic here would otherwise kill the API (no request handler's recover covers this goroutine)
		if r := recover(); r != nil {
			slog.Error("terraform estimate panicked", "lab", lab.Dir, "panic", r)
			c.err = fmt.Errorf("estimate failed: %v", r)
		}
		e.finish(lab.Dir, c)
	}()
	c.b, c.err = e.estimate(ctx, lab)
}

func (e *TerraformEstimator) estimate(ctx context.Context, lab *content.Lab) (pricing.Breakdown, error) {
	plan, err := e.Plan.Plan(ctx, lab)
	if err != nil {
		return pricing.Breakdown{}, err
	}
	res, err := pricing.FromPlan(plan)
	if err != nil {
		return pricing.Breakdown{}, err
	}
	prices, err := e.Prices(ctx)
	if err != nil {
		return pricing.Breakdown{}, err
	}
	b, err := pricing.PriceAWS(res, lab.AWS.Region, lab.AWS.Prices, lab.AWS.Usage, prices)
	if err != nil {
		return pricing.Breakdown{}, err
	}
	switch h, limit := b.HourlyUSD, lab.AWS.MaxHourlyUSD; {
	case !(h > 0): // NaN too
		return pricing.Breakdown{}, fmt.Errorf("the plan priced to $%.4f an hour; an aws lab is never quoted as free", h)
	case h > limit:
		return pricing.Breakdown{}, apperr.Wrap(apperr.Invalid,
			fmt.Sprintf("the estimate $%.4f an hour is over this lab's max_hourly_usd $%.4f", h, limit))
	}
	return b, nil
}

// finish records c's outcome, frees its slot and wakes its waiters.
func (e *TerraformEstimator) finish(key string, c *estimateCall) {
	e.mu.Lock()
	if c.err != nil {
		e.failed[key] = estimateFailure{c.err, e.now().Add(estimateFailTTL)}
	} else {
		e.cache[key] = c.b
	}
	delete(e.inflight, key)
	e.running--
	e.mu.Unlock()
	close(c.done)
}
