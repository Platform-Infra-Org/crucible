package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"crucible/internal/awscloud"
	"crucible/internal/labs"
)

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestInfracostOffOnlyWithDryrun(t *testing.T) {
	newCloud := func(context.Context, awscloud.Config) (awscloud.Cloud, error) { return &awscloud.Fake{}, nil }
	env := envOf(map[string]string{"CRUCIBLE_INFRACOST": "off", "CRUCIBLE_AWS_STATE_BUCKET": "b", "CRUCIBLE_AWS_STATE_REGION": "eu-west-1"})
	_, err := awsSetup(context.Background(), "1", &labs.AWSRunner{StateBucket: "b", StateRegion: "eu-west-1"}, labs.FixedRates{}, env, newCloud)
	if err == nil || !strings.Contains(err.Error(), "only allowed with CRUCIBLE_AWS_LABS=dryrun") {
		t.Fatalf("real aws labs with a $0 estimate must refuse to start: %v", err)
	}
	if est, err := awsSetup(context.Background(), "dryrun", &labs.AWSRunner{}, labs.FixedRates{}, env, newCloud); err != nil || est == nil {
		t.Fatalf("dryrun prices from the dev rates: %v %v", est, err)
	}
}

func TestDryrunNeverConstructsTheSDKClient(t *testing.T) {
	ar := &labs.AWSRunner{}
	_, err := awsSetup(context.Background(), "dryrun", ar, labs.FixedRates{}, envOf(nil),
		func(context.Context, awscloud.Config) (awscloud.Cloud, error) {
			t.Fatal("dryrun called the real AWS client constructor")
			return nil, nil
		})
	if _, fake := ar.Cloud.(*awscloud.Fake); err != nil || !fake || ar.DryRun == nil {
		t.Fatalf("dryrun runs on the fake cloud: %T %v", ar.Cloud, err)
	}
}

// Leaked lab credentials live up to an hour after End; an hourly reaper bounds their leftovers at about 2 h.
// Only Cost Explorer costs money, so it stays at 6 h. jobs.New makes each one unique per its period.
func TestReaperRunsHourlyCostExplorerEverySixHours(t *testing.T) {
	every := map[string]time.Duration{}
	for _, p := range periodicJobs() {
		every[p.Args.Kind()] = p.Every
	}
	if every[labs.ReapArgs{}.Kind()] != time.Hour || every[labs.CostArgs{}.Kind()] != 6*time.Hour ||
		every[labs.SweepArgs{}.Kind()] != 15*time.Second || every[labs.BudgetArgs{}.Kind()] != 5*time.Minute {
		t.Fatalf("periodic jobs: %v", every)
	}
}

func TestPreviewGuard(t *testing.T) {
	tok := strings.Repeat("a", 48)
	base := map[string]string{"CRUCIBLE_PREVIEW_TOKEN": tok, "CRUCIBLE_PUBLIC_URL": "http://localhost:8090"}
	with := func(k, v string) map[string]string {
		m := map[string]string{}
		for a, b := range base {
			m[a] = b
		}
		m[k] = v
		return m
	}
	for name, tc := range map[string]struct {
		env     map[string]string
		wantErr bool
	}{
		"ok":               {base, false},
		"no token is off":  {map[string]string{"OIDC_ISSUER": "https://idp"}, false},
		"public url unset": {with("CRUCIBLE_PUBLIC_URL", ""), true},
		"non-loopback":     {with("CRUCIBLE_PUBLIC_URL", "http://crucible.example.com"), true},
		"https":            {with("CRUCIBLE_PUBLIC_URL", "https://localhost"), true},
		"oidc issuer set":  {with("OIDC_ISSUER", "https://idp"), true},
		"oidc client set":  {with("OIDC_CLIENT_ID", "x"), true},
		"oidc secret set":  {with("OIDC_CLIENT_SECRET", "x"), true},
		"oidc discovery":   {with("OIDC_DISCOVERY_URL", "http://kc"), true},
	} {
		err := previewGuard(envOf(tc.env))
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err=%v wantErr=%v", name, err, tc.wantErr)
		}
	}
}
