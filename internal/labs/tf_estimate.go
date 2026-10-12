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
	estimateFailTTL = 5 * time.Minute // how long a failed plan is remembered
	pricesTTL       = time.Minute     // how long a fetched price list is reused
)

// Planner runs a read-only `terraform plan` of an aws lab's module and returns `terraform show -json` of it.
type Planner interface {
	Plan(ctx context.Context, lab *content.Lab) ([]byte, error)
}

// TerraformEstimator prices aws labs (spec 2026-10-12 §2): a read-only plan of the module, priced by Crucible from
// the free AWS Price List, with the lab's declared prices and usage. It fails closed: a failed plan, stale or missing
// prices, a type Crucible can't price, a $0 total or a total over the lab's max_hourly_usd is an error, never a price.
// Trainee lobby views call it, so plans are bounded: a plan's resources are cached per content version (lab.Dir is an
// immutable export, so the plan runs once per lab per pushed commit), concurrent views of one version share one run,
// failed plans are remembered for estimateFailTTL, and at most estimateSlots plans go at once; a view that finds no
// free slot is told to retry rather than queueing. A plan is detached from the request that started it (the Planner
// bounds its own time), so a closed tab does not waste it. The price is not cached: every call prices the resources
// with the current list (reused for pricesTTL, a failure never), so stale prices block a lab priced before and a new
// weekly price reaches it.
// ponytail: the caches never shrink (one entry per lab version).
type TerraformEstimator struct {
	Plan   Planner
	Prices func(context.Context) (pricing.Lookup, error)
	Now    func() time.Time // nil = time.Now

	mu          sync.Mutex
	cache       map[string][]pricing.Resource
	failed      map[string]estimateFailure
	inflight    map[string]*estimateCall
	running     int
	prices      pricing.Lookup
	pricesUntil time.Time
}

type estimateFailure struct {
	err   error
	until time.Time
}

type estimateCall struct {
	done chan struct{}
	res  []pricing.Resource
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

// Breakdown is the lab's planned resources priced now, line by line (HourlyUSD is its total).
func (e *TerraformEstimator) Breakdown(ctx context.Context, lab *content.Lab) (pricing.Breakdown, error) {
	if lab.AWS == nil {
		return pricing.Breakdown{}, errors.New("not an aws lab")
	}
	res, err := e.resources(ctx, lab)
	if err != nil {
		return pricing.Breakdown{}, err
	}
	prices, err := e.lookup(ctx)
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
		return pricing.Breakdown{}, apperr.Wrap(apperr.Invalid, overCeiling(h, limit))
	}
	return b, nil
}

// lookup is the current price list, fetched at most once per pricesTTL; a failed fetch is not kept.
func (e *TerraformEstimator) lookup(ctx context.Context) (pricing.Lookup, error) {
	e.mu.Lock()
	if e.prices != nil && e.now().Before(e.pricesUntil) {
		l := e.prices
		e.mu.Unlock()
		return l, nil
	}
	e.mu.Unlock()
	l, err := e.Prices(ctx)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	e.prices, e.pricesUntil = l, e.now().Add(pricesTTL)
	e.mu.Unlock()
	return l, nil
}

// resources is the lab version's planned resources: one bounded, shared, cached plan per content version.
func (e *TerraformEstimator) resources(ctx context.Context, lab *content.Lab) ([]pricing.Resource, error) {
	key := lab.Dir
	e.mu.Lock()
	if res, ok := e.cache[key]; ok {
		e.mu.Unlock()
		return res, nil
	}
	if f, ok := e.failed[key]; ok && e.now().Before(f.until) {
		e.mu.Unlock()
		return nil, f.err
	}
	c := e.inflight[key]
	if c == nil {
		if e.running >= estimateSlots {
			e.mu.Unlock()
			return nil, apperr.Wrap(apperr.Unavailable, "estimate pending, try again shortly")
		}
		if e.inflight == nil {
			e.cache, e.failed, e.inflight = map[string][]pricing.Resource{}, map[string]estimateFailure{}, map[string]*estimateCall{}
		}
		c = &estimateCall{done: make(chan struct{})}
		e.inflight[key] = c
		e.running++
		go e.run(context.WithoutCancel(ctx), lab, c)
	}
	e.mu.Unlock()
	select {
	case <-c.done:
		return c.res, c.err
	case <-ctx.Done():
		return nil, ctx.Err()
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
	c.res, c.err = e.plan(ctx, lab)
}

func (e *TerraformEstimator) plan(ctx context.Context, lab *content.Lab) ([]pricing.Resource, error) {
	plan, err := e.Plan.Plan(ctx, lab)
	if err != nil {
		return nil, err
	}
	return pricing.FromPlan(plan)
}

// finish records c's outcome, frees its slot and wakes its waiters.
func (e *TerraformEstimator) finish(key string, c *estimateCall) {
	e.mu.Lock()
	if c.err != nil {
		e.failed[key] = estimateFailure{c.err, e.now().Add(estimateFailTTL)}
	} else {
		e.cache[key] = c.res
	}
	delete(e.inflight, key)
	e.running--
	e.mu.Unlock()
	close(c.done)
}
