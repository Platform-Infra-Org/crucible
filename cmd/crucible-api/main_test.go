package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"crucible/internal/awscloud"
	"crucible/internal/labs"
	"crucible/internal/pricing"
)

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func noPrices(context.Context) (pricing.Lookup, error) { return nil, pricing.ErrStale }

// real labs: the variables every real aws lab install sets (deploy/aws/labs outputs).
var real = map[string]string{"CRUCIBLE_AWS_LAB_ROLE_ARN": "lab", "CRUCIBLE_AWS_OPS_ROLE_ARN": "ops", "CRUCIBLE_AWS_PLAN_ROLE_ARN": "plan",
	"CRUCIBLE_AWS_EXPORT_BUCKET": "exports", "CRUCIBLE_AWS_EXPORT_NAME": "crucible-labs"}

func without(k string) map[string]string {
	m := map[string]string{}
	for a, b := range real {
		if a != k {
			m[a] = b
		}
	}
	return m
}

// Without the plan role there is no estimate, and without the export no billed spend: refuse to start, naming it.
func TestAWSSetupNeedsThePlanRoleAndExport(t *testing.T) {
	newCloud := func(context.Context, awscloud.Config) (awscloud.Cloud, error) { return &awscloud.Fake{}, nil }
	for _, k := range []string{"CRUCIBLE_AWS_PLAN_ROLE_ARN", "CRUCIBLE_AWS_EXPORT_BUCKET", "CRUCIBLE_AWS_EXPORT_NAME"} {
		_, _, err := awsSetup(context.Background(), "1", &labs.AWSRunner{StateBucket: "b", StateRegion: "eu-west-1"}, labs.FixedRates{},
			envOf(without(k)), newCloud, noPrices)
		if err == nil || !strings.Contains(err.Error(), k) {
			t.Errorf("missing %s must refuse to start, naming it: %v", k, err)
		}
	}
}

// A dry run prices aws labs from CRUCIBLE_DEV_LAB_USD_PER_HOUR and never downloads the price list.
func TestDryRunPricesFromDevRates(t *testing.T) {
	rates := labs.FixedRates{"cloud-heat": 0.04}
	est, fetch, err := awsSetup(context.Background(), "dryrun", &labs.AWSRunner{}, rates, envOf(nil), nil, noPrices)
	if fr, ok := est.(labs.FixedRates); err != nil || !ok || fr["cloud-heat"] != 0.04 {
		t.Fatalf("dryrun estimator: %T %v %v", est, est, err)
	}
	if fetch != nil {
		t.Fatal("a dry run never constructs a price fetcher")
	}
}

// The dry-run fixture prices what the fake apply leaves in state (a t3.nano with an 8 GiB gp3 root and a bucket), in
// the regions the examples use, so metering works in a dry run.
func TestDryRunFixturePricesTheFakeApply(t *testing.T) {
	snap := pricing.Snapshot{}
	for _, r := range pricing.Fixture() {
		snap[[3]string{r.Service, r.Region, r.Key}] = r.USD
	}
	for _, region := range []string{"eu-west-1", "us-east-1"} {
		fake := &awscloud.Fake{}
		fake.SimulateApply(region, awscloud.Session{LabID: "abc"})
		st, err := fake.ReadState(context.Background(), "", "", "labs/abc.tfstate")
		if err != nil {
			t.Fatal(err)
		}
		res, err := pricing.FromState(st)
		if err != nil {
			t.Fatal(err)
		}
		if b, err := pricing.PriceAWS(res, region, nil, nil, snap); err != nil || b.HourlyUSD <= 0 {
			t.Fatalf("%s: %+v %v", region, b, err)
		}
	}
}

// Real aws labs are priced by Crucible from a read-only terraform plan run with the plan role.
func TestRealAWSLabsEstimateFromATerraformPlan(t *testing.T) {
	var got awscloud.Config
	newCloud := func(_ context.Context, c awscloud.Config) (awscloud.Cloud, error) {
		got = c
		return &awscloud.Fake{}, nil
	}
	ar := &labs.AWSRunner{StateBucket: "b", StateRegion: "eu-west-1"}
	est, fetch, err := awsSetup(context.Background(), "1", ar, labs.FixedRates{}, envOf(real), newCloud, noPrices)
	te, ok := est.(*labs.TerraformEstimator)
	if err != nil || !ok || te.Plan != labs.Planner(ar) || te.Prices == nil {
		t.Fatalf("the terraform estimator plans with the aws runner: %T %v", est, err)
	}
	if got.LabRoleARN != "lab" || got.OpsRoleARN != "ops" || got.PlanRoleARN != "plan" ||
		got.ExportBucket != "exports" || got.ExportPrefix != "cur" || got.ExportName != "crucible-labs" {
		t.Fatalf("roles and export (prefix defaults to cur): %+v", got)
	}
	if fetch == nil || fetch.Base != "https://pricing.us-east-1.amazonaws.com" {
		t.Fatalf("real labs refresh prices from the public AWS Price List: %+v", fetch)
	}
	if _, err := te.Prices(context.Background()); err != pricing.ErrStale {
		t.Fatalf("prices come from the store: %v", err)
	}
}

func TestDryrunNeverConstructsTheSDKClient(t *testing.T) {
	ar := &labs.AWSRunner{}
	_, _, err := awsSetup(context.Background(), "dryrun", ar, labs.FixedRates{}, envOf(nil),
		func(context.Context, awscloud.Config) (awscloud.Cloud, error) {
			t.Fatal("dryrun called the real AWS client constructor")
			return nil, nil
		}, noPrices)
	if _, fake := ar.Cloud.(*awscloud.Fake); err != nil || !fake || ar.DryRun == nil {
		t.Fatalf("dryrun runs on the fake cloud: %T %v", ar.Cloud, err)
	}
}

// Leaked lab credentials live up to an hour after End; an hourly reaper bounds their leftovers at about 2 h.
// Cost ingestion lists and reads S3 files, so it stays at 6 h. jobs.New makes each one unique per its period.
func TestReaperRunsHourlyBilledCostsEverySixHours(t *testing.T) {
	every := map[string]time.Duration{}
	for _, p := range periodicJobs() {
		every[p.Args.Kind()] = p.Every
	}
	if every[labs.ReapArgs{}.Kind()] != time.Hour || every[labs.CostArgs{}.Kind()] != 6*time.Hour ||
		every[labs.SweepArgs{}.Kind()] != 15*time.Second || every[labs.BudgetArgs{}.Kind()] != 5*time.Minute ||
		every[labs.PriceArgs{}.Kind()] != 7*24*time.Hour {
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
