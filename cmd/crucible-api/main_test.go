package main

import (
	"context"
	"strings"
	"testing"

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
