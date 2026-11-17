package awsops

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// An install's details normally come from /<name>/install, which the four
// Terraform commands publish. But an install made before that parameter
// existed — or by a colleague running an older Crucible — has none, and the
// operator who needs it most is the one holding only this binary and no
// Terraform. So the facts that are on the node itself are read from the node:
// the same tag discovery `crucible aws connect` has always used.
//
// This is a fallback, not a replacement. It can answer "which node, where, at
// what address, reachable how" and nothing else: the Cognito pool, the state
// bucket and the lab role live only in the parameter, so the commands that need
// those still ask for an `up`.

// discover reads the install's basics from AWS: the node named after the
// install, its state and the address its tag advertises.
func (o Ops) discover(ctx context.Context) (map[string]string, error) {
	name, region := o.installName(), o.installRegion()
	if region == "" {
		return nil, errors.New("no region: pass --region, or set install.region in crucible.yaml")
	}
	nodes, err := o.AWS.In(region).FindInstances(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("looking for the %s node in %s: %w", name, region, err)
	}
	// Terminated instances linger in the API for an hour and are not an install.
	live := nodes[:0]
	for _, n := range nodes {
		if n.State != "terminated" && n.State != "shutting-down" {
			live = append(live, n)
		}
	}
	switch {
	case len(live) == 0:
		return nil, fmt.Errorf("no install: %s is not in SSM and no instance named %s is running in %s "+
			"(wrong region or account? `crucible aws where` prints what this binary is using)", o.installParam(), name, region)
	case len(live) > 1:
		ids := make([]string, len(live))
		for i, n := range live {
			ids[i] = n.ID
		}
		return nil, fmt.Errorf("%d instances are named %s in %s (%s): run `crucible aws up` so the install's details are in SSM, "+
			"because the tags alone cannot say which one is the install", len(live), name, region, strings.Join(ids, "; "))
	}
	n := live[0]
	m := map[string]string{
		"name":        name,
		"region":      region,
		"instance_id": n.ID,
		"url":         n.PublicURL,
		"access":      "public",
	}
	if strings.HasPrefix(n.PublicURL, "http://localhost:") {
		m["access"] = "ssm" // the tag says so: there is no inbound path
	}
	return m, nil
}

// installOrDiscover is the install parameter when it exists and tag discovery
// when it does not, for the commands that need only to find the node. It says
// which it used, so a thinner report is never read as a healthy one.
func (o Ops) installOrDiscover(ctx context.Context) (map[string]string, error) {
	m, err := o.install(ctx)
	if err == nil {
		return m, nil
	}
	if !errors.Is(err, ErrNoInstall) {
		return nil, err
	}
	d, derr := o.discover(ctx)
	if derr != nil {
		return nil, derr
	}
	fmt.Fprintf(o.Log, "%s is not in SSM yet, so this is what the node's tags say; "+
		"run `crucible aws up` once with this version of Crucible for the rest\n", o.installParam())
	return d, nil
}
