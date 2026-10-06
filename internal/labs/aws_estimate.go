package labs

import (
	"context"
	"errors"
	"path/filepath"
	"sync"

	"crucible/internal/content"
	"crucible/internal/infracost"
)

// InfracostEstimator prices aws labs (spec §9.1). Results are cached per content version: lab.Dir is an immutable
// export, so the CLI runs once per lab per pushed commit (the first lobby view after a sync waits for it).
// Failures are not cached. The CLI has its own timeout (infracost.Exec).
// ponytail: the cache never shrinks (one float per lab version); concurrent first views may each run the CLI.
type InfracostEstimator struct {
	Run infracost.Runner

	mu    sync.Mutex
	cache map[string]float64
}

func (e *InfracostEstimator) HourlyUSD(ctx context.Context, lab *content.Lab) (float64, error) {
	if lab.AWS == nil {
		return 0, errors.New("not an aws lab")
	}
	e.mu.Lock()
	h, ok := e.cache[lab.Dir]
	e.mu.Unlock()
	if ok {
		return h, nil
	}
	h, err := infracost.Hourly(ctx, e.Run, filepath.Join(lab.Dir, "terraform"), lab.AWS.Region)
	if err != nil {
		return 0, err
	}
	e.mu.Lock()
	if e.cache == nil {
		e.cache = map[string]float64{}
	}
	e.cache[lab.Dir] = h
	e.mu.Unlock()
	return h, nil
}
