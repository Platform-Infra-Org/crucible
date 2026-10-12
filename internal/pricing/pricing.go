// Package pricing turns Terraform resources into hourly prices from list prices Crucible downloads for free
// (spec 2026-10-12 §2). A resource it can't price is an error, never $0. The only defaults: an instance's root volume is
// 8 GiB gp3 when the plan doesn't say, and a load balancer spans at least 2 subnets (metering corrects both from the state).
package pricing

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

const HoursPerMonth = 730.0

type Resource struct {
	Address string
	Type    string
	Values  map[string]any
	Unknown map[string]any // plan only: attributes unknown until apply (after_unknown)
}

type Lookup interface {
	Price(service, region, key string) (float64, bool)
}

type Line struct {
	Address        string  `json:"address"`
	Type           string  `json:"type"`
	HourlyUSD      float64 `json:"hourly_usd"`
	UsageHourlyUSD float64 `json:"usage_hourly_usd"`
	Note           string  `json:"note,omitempty"` // "free", "declared", "usage not stated", …
}

type Breakdown struct {
	Lines     []Line  `json:"lines"`
	HourlyUSD float64 `json:"hourly_usd"`
}

// Unpriced lists resource types Crucible can't price and the lab doesn't declare.
type Unpriced struct{ Types []string }

func (u *Unpriced) Error() string {
	return "no price for " + strings.Join(u.Types, ", ") + ": declare it under aws.prices (USD per hour)"
}

// FromPlan reads `terraform show -json` output: every managed resource the plan creates or updates
// (a replace includes "create"); an estimate plans against an empty state, so that is what will exist.
func FromPlan(b []byte) ([]Resource, error) {
	var p struct {
		ResourceChanges []struct {
			Address string `json:"address"`
			Mode    string `json:"mode"`
			Type    string `json:"type"`
			Change  struct {
				Actions []string       `json:"actions"`
				After   map[string]any `json:"after"`
				Unknown map[string]any `json:"after_unknown"`
			} `json:"change"`
		} `json:"resource_changes"`
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("reading the plan: %w", err)
	}
	var out []Resource
	for _, rc := range p.ResourceChanges {
		// an estimate plans against an empty state, so what will exist is what the plan creates or updates
		if rc.Mode != "managed" || !(slices.Contains(rc.Change.Actions, "create") || slices.Contains(rc.Change.Actions, "update")) {
			continue
		}
		out = append(out, Resource{Address: rc.Address, Type: rc.Type, Values: rc.Change.After, Unknown: rc.Change.Unknown})
	}
	return out, nil
}

// FromState reads a Terraform state (version 4): every managed resource instance.
func FromState(b []byte) ([]Resource, error) {
	var s struct {
		Resources []struct {
			Module    string `json:"module"`
			Mode      string `json:"mode"`
			Type      string `json:"type"`
			Name      string `json:"name"`
			Instances []struct {
				IndexKey   any            `json:"index_key"`
				Attributes map[string]any `json:"attributes"`
			} `json:"instances"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("reading the state: %w", err)
	}
	var out []Resource
	for _, r := range s.Resources {
		if r.Mode != "managed" {
			continue
		}
		base := r.Type + "." + r.Name
		if r.Module != "" {
			base = r.Module + "." + base
		}
		for _, in := range r.Instances {
			addr := base
			switch k := in.IndexKey.(type) {
			case float64:
				addr = fmt.Sprintf("%s[%d]", base, int(k))
			case string:
				addr = fmt.Sprintf("%s[%q]", base, k)
			}
			out = append(out, Resource{Address: addr, Type: r.Type, Values: in.Attributes})
		}
	}
	return out, nil
}

// usageFor finds the stated monthly usage for an address, by exact address or the address without its index.
func usageFor(usage map[string]map[string]float64, addr string) map[string]float64 {
	if u, ok := usage[addr]; ok {
		return u
	}
	if i := strings.LastIndex(addr, "["); i > 0 && strings.HasSuffix(addr, "]") {
		return usage[addr[:i]]
	}
	return nil
}

func str(v map[string]any, k string) string { s, _ := v[k].(string); return s }

func num(v map[string]any, k string) float64 {
	switch n := v[k].(type) {
	case float64:
		return n
	}
	return 0
}

// first returns the first element of a nested block list (Terraform renders blocks as lists).
func first(v map[string]any, k string) map[string]any {
	if l, ok := v[k].([]any); ok && len(l) > 0 {
		m, _ := l[0].(map[string]any)
		return m
	}
	return nil
}
