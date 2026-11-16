package awsops

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestInstallParameterRoundTrips(t *testing.T) {
	r := &recorder{output: fakeAWS}
	o := r.ops(t.TempDir())
	if err := o.publishInstall(context.Background(), map[string]string{
		"region": "eu-west-1", "instance_id": "i-123", "data_bucket": "bkt",
	}); err != nil {
		t.Fatalf("publishInstall: %v", err)
	}
	if indexOf(r.calls, "put-parameter --region eu-west-1 --name /crucible/install") < 0 {
		t.Fatalf("want a write to /crucible/install:\n%s", strings.Join(r.calls, "\n"))
	}
	got, err := o.install(context.Background())
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if got["instance_id"] != "i-123" || got["data_bucket"] != "bkt" {
		t.Errorf("install = %v, want the published values", got)
	}
}

func TestPublishInstallMergesOverWhatIsThere(t *testing.T) {
	// init publishes the persistent half and up the main half, in that order,
	// so the second must not erase the first.
	r := &recorder{output: fakeAWS}
	o := r.ops(t.TempDir())
	if err := o.publishInstall(context.Background(), map[string]string{"cognito_user_pool_id": "pool-1"}); err != nil {
		t.Fatal(err)
	}
	if err := o.publishInstall(context.Background(), map[string]string{"instance_id": "i-123"}); err != nil {
		t.Fatal(err)
	}
	got, err := o.install(context.Background())
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if got["cognito_user_pool_id"] != "pool-1" || got["instance_id"] != "i-123" {
		t.Errorf("install = %v, want both halves", got)
	}
}

// Review Focus 4: an install made by an older release has no parameter yet.
func TestInstallSaysToRunUpWhenAbsent(t *testing.T) {
	r := &recorder{output: fakeAWS, outErr: func(cmd string) error {
		if strings.Contains(cmd, "/crucible/install") {
			return errors.New("An error occurred (ParameterNotFound) when calling GetParameter")
		}
		return nil
	}}
	_, err := r.ops(t.TempDir()).install(context.Background())
	if !errors.Is(err, ErrNoInstall) {
		t.Fatalf("install error = %v, want ErrNoInstall", err)
	}
	if !strings.Contains(err.Error(), "crucible aws up") {
		t.Errorf("error %q must name `crucible aws up`", err)
	}
}

func TestInstallDoesNotTouchTheEnvParameter(t *testing.T) {
	// /<name>/env is the node's own contract, read by deploy.sh.
	r := &recorder{output: fakeAWS}
	o := r.ops(t.TempDir())
	if err := o.publishInstall(context.Background(), map[string]string{"region": "eu-west-1"}); err != nil {
		t.Fatal(err)
	}
	if indexOf(r.calls, "/crucible/env") >= 0 {
		t.Fatalf("the env parameter must not be written:\n%s", strings.Join(r.calls, "\n"))
	}
}

// The point of the whole task: an operator who only runs the everyday commands
// never needs Terraform.
func TestEverydayOperationsRunWithoutTerraform(t *testing.T) {
	published := map[string]string{
		"region": "eu-west-1", "name": "crucible", "instance_id": "i-123",
		"data_bucket": "bkt", "url": "https://c.example.com", "domain": "c.example.com",
		"access": "public", "cognito_user_pool_id": "eu-west-1_abc",
		"state_bucket": "st",
	}
	for _, tc := range []struct {
		name string
		run  func(Ops) error
	}{
		{"status", func(o Ops) error { return o.Status(context.Background()) }},
		{"deploy", func(o Ops) error { return o.Deploy(context.Background(), DeployOpts{Version: "v1.4.0"}) }},
		{"snapshot", func(o Ops) error { return o.Snapshot(context.Background()) }},
		{"wake", func(o Ops) error { return o.Wake(context.Background()) }},
		{"invite", func(o Ops) error { return o.Invite(context.Background(), "someone@example.com", "") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &recorder{output: func(cmd string) string {
				if strings.Contains(cmd, "/crucible/install") {
					return installJSON(published)
				}
				return fakeAWS(cmd)
			}}
			o := r.ops(t.TempDir())
			o.Exec = func(_ context.Context, _ string, name string, args ...string) error {
				if name == "terraform" {
					t.Fatalf("%s ran terraform: %s %v", tc.name, name, args)
				}
				r.calls = append(r.calls, name+" "+strings.Join(args, " "))
				return nil
			}
			o.Output = func(_ context.Context, _ string, name string, args ...string) (string, error) {
				if name == "terraform" {
					t.Fatalf("%s ran terraform: %s %v", tc.name, name, args)
				}
				cmd := name + " " + strings.Join(args, " ")
				r.calls = append(r.calls, cmd)
				return r.output(cmd), nil
			}
			if err := tc.run(o); err != nil {
				t.Fatalf("%s: %v\n%s", tc.name, err, strings.Join(r.calls, "\n"))
			}
		})
	}
}

// Only init, labs-init, up and teardown may require Terraform. Everything else
// must work on a machine where it was never installed.
func TestTerraformMissingOnlyAffectsTheFourCommands(t *testing.T) {
	needs := map[string]func(Ops) error{
		"init": func(o Ops) error {
			return o.Init(context.Background(), InitOpts{Region: "eu-west-1", Domain: "c.example.com"})
		},
		"labs-init": func(o Ops) error {
			return o.LabsInit(context.Background(), "eu-west-1", "", 0, nil, nil, LabNet{}, nil)
		},
		"up":       func(o Ops) error { return o.Up(context.Background(), "x.tfvars", true, false, DeployOpts{}) },
		"teardown": func(o Ops) error { return o.Teardown(context.Background(), "x.tfvars", true, true, true) },
	}
	free := map[string]func(Ops) error{
		"status":   func(o Ops) error { return o.Status(context.Background()) },
		"deploy":   func(o Ops) error { return o.Deploy(context.Background(), DeployOpts{Version: "v1.4.0"}) },
		"snapshot": func(o Ops) error { return o.Snapshot(context.Background()) },
		"wake":     func(o Ops) error { return o.Wake(context.Background()) },
		"invite":   func(o Ops) error { return o.Invite(context.Background(), "someone@example.com", "") },
	}
	noTerraform := func(t *testing.T) Ops {
		r := &recorder{output: fakeAWS}
		o := r.ops(t.TempDir())
		o.LookPath = func(file string) (string, error) {
			if file == "terraform" {
				return "", errors.New("executable file not found in $PATH")
			}
			return "/usr/local/bin/" + file, nil
		}
		return o
	}
	for name, run := range needs {
		t.Run(name+" needs it", func(t *testing.T) {
			err := run(noTerraform(t))
			if err == nil || !strings.Contains(err.Error(), "terraform is not on your PATH") {
				t.Fatalf("%s must say terraform is missing: %v", name, err)
			}
			if !strings.Contains(err.Error(), terraformInstallURL) {
				t.Errorf("%s must name where to get it: %v", name, err)
			}
		})
	}
	for name, run := range free {
		t.Run(name+" does not", func(t *testing.T) {
			if err := run(noTerraform(t)); err != nil && strings.Contains(err.Error(), "terraform") {
				t.Fatalf("%s must not need terraform: %v", name, err)
			}
		})
	}
}
