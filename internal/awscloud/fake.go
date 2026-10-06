package awscloud

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
)

// Fake is an in-memory lab account. Deletes follow the lab role's rule: a session may delete only its own lab's
// resources. It backs unit tests and CRUCIBLE_AWS_LABS=dryrun (see SimulateApply).
type Fake struct {
	Now func() time.Time
	Err error // when set, every call fails with it (throttling, Cost Explorer down…)

	mu        sync.Mutex
	resources map[string]fakeRes // by ARN
	assumed   []Session
	costs     []DailyCost
	events    []TrailEvent
	notYet    map[string]int // ARN → deletes still refused with ErrNotYet
}

type fakeRes struct {
	region string
	r      Resource
}

var _ Cloud = (*Fake)(nil)

func (f *Fake) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

func (f *Fake) Add(region string, r Resource) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.resources == nil {
		f.resources = map[string]fakeRes{}
	}
	f.resources[r.ARN] = fakeRes{region, r}
}

func (f *Fake) AddCost(c DailyCost) { f.mu.Lock(); defer f.mu.Unlock(); f.costs = append(f.costs, c) }

func (f *Fake) AddEvent(e TrailEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, e)
}

// NotYet makes the next n deletes of arn fail with ErrNotYet (a volume still attached, a security group in use).
func (f *Fake) NotYet(arn string, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.notYet == nil {
		f.notYet = map[string]int{}
	}
	f.notYet[arn] = n
}

func (f *Fake) Has(arn string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.resources[arn]
	return ok
}

func (f *Fake) Assumed() []Session { f.mu.Lock(); defer f.mu.Unlock(); return slices.Clone(f.assumed) }

func token(labID string) string { return "fake-token-" + labID }

func (f *Fake) AssumeLab(_ context.Context, s Session) (Credentials, error) {
	if !ValidLabID(s.LabID) {
		return Credentials{}, fmt.Errorf("invalid lab id %q", s.LabID)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return Credentials{}, f.Err
	}
	f.assumed = append(f.assumed, s)
	return Credentials{AccessKeyID: "ASIAFAKE" + strings.ToUpper(s.LabID[:8]), SecretAccessKey: "fake-secret",
		SessionToken: token(s.LabID), Expires: f.now().Add(time.Hour)}, nil
}

func (f *Fake) Tagged(_ context.Context, region, labID string) ([]Resource, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return nil, f.Err
	}
	var out []Resource
	for _, x := range f.resources {
		if x.region == region && (labID == "" || x.r.LabID == labID) {
			out = append(out, x.r)
		}
	}
	slices.SortFunc(out, func(a, b Resource) int { return strings.Compare(a.ARN, b.ARN) })
	return out, nil
}

func (f *Fake) Delete(_ context.Context, region string, c Credentials, arn string) (bool, error) {
	if t, _ := kind(arn); t == "" {
		return false, ErrUnsupported
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return false, f.Err
	}
	x, ok := f.resources[arn]
	if !ok || x.region != region {
		return false, nil
	}
	if c.SessionToken != token(x.r.LabID) {
		return false, errors.New("AccessDenied: not this lab's resource")
	}
	if f.notYet[arn] > 0 {
		f.notYet[arn]--
		return false, fmt.Errorf("%w: DependencyViolation (fake)", ErrNotYet)
	}
	delete(f.resources, arn)
	return true, nil
}

func (f *Fake) Costs(_ context.Context, from, to time.Time) ([]DailyCost, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return nil, f.Err
	}
	var out []DailyCost
	for _, c := range f.costs {
		if !c.Day.Before(from) && c.Day.Before(to) {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *Fake) LabWrites(_ context.Context, _ string, since time.Time) ([]TrailEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return nil, f.Err
	}
	var out []TrailEvent
	for _, e := range f.events {
		if !e.At.Before(since) {
			out = append(out, e)
		}
	}
	return out, nil
}

// SimulateApply stands in for `terraform apply` in dry-run mode. It adds the lab's bucket (managed by terraform)
// and a volume "made by hand in the workspace", which only the destroy-time tag sweep removes. It also books $0.11
// of cost today, so the Ledger has an actual to show.
func (f *Fake) SimulateApply(region string, s Session) {
	f.Add(region, Resource{ARN: "arn:aws:s3:::crucible-lab-" + s.LabID, LabID: s.LabID})
	f.Add(region, Resource{ARN: fmt.Sprintf("arn:aws:ec2:%s:000000000000:volume/vol-%s", region, s.LabID), LabID: s.LabID})
	f.AddCost(DailyCost{Day: f.now().UTC().Truncate(24 * time.Hour), LabID: s.LabID, USD: 0.11})
}

// SimulateDestroy stands in for `terraform destroy`: it removes only what terraform manages (the bucket).
func (f *Fake) SimulateDestroy(labID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.resources, "arn:aws:s3:::crucible-lab-"+labID)
}
