package awsops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
)

// ErrNoInstall is an install created before the install parameter existed. The
// first command run against it must be an up, which publishes the parameter.
var ErrNoInstall = errors.New("this install's details are not in SSM yet: run `crucible aws up` once with this version of Crucible")

// terraformVersion is the version the stacks require (deploy/aws/*/versions.tf
// pins required_version = ">= 1.10"). Only the four commands that create or
// destroy resources need it.
const terraformVersion = "1.10 or later"

const terraformInstallURL = "https://developer.hashicorp.com/terraform/install"

// installName is the stack name, which also prefixes every SSM parameter.
func (o Ops) installName() string {
	if o.Config != nil && o.Config.Install.Name != "" {
		return o.Config.Install.Name
	}
	return "crucible"
}

func (o Ops) installParam() string { return "/" + o.installName() + "/install" }

// installRegion is where the parameter lives: the configured region, else
// whatever the credentials resolved to.
func (o Ops) installRegion() string {
	if o.Config != nil && o.Config.Install.Region != "" {
		return o.Config.Install.Region
	}
	if o.AWS != nil {
		return o.AWS.Region()
	}
	return ""
}

// publishInstall records the install's details in SSM after a successful apply,
// merged over what is already there: init publishes the persistent stack's
// outputs and up the main stack's, so neither may erase the other.
//
// /<name>/env is deliberately untouched. That parameter is the node's own
// contract, read by deploy.sh, and it carries none of state_bucket,
// cognito_user_pool_id, local_port or subnet_id.
func (o Ops) publishInstall(ctx context.Context, m map[string]string) error {
	region := m["region"]
	if region == "" {
		region = o.installRegion()
	}
	api := o.AWS.In(region)
	merged := map[string]string{}
	if raw, err := api.GetParameter(ctx, o.installParam(), false); err == nil && raw != "" {
		_ = json.Unmarshal([]byte(raw), &merged) // a corrupt value is replaced, not fatal
	}
	for k, v := range m {
		if v != "" {
			merged[k] = v
		}
	}
	b, err := json.Marshal(merged)
	if err != nil {
		return err
	}
	if err := api.PutParameter(ctx, o.installParam(), string(b), false); err != nil {
		return fmt.Errorf("applied, but recording the install's details failed: %w", err)
	}
	return nil
}

// install is the everyday commands' replacement for reading Terraform outputs.
// It reads the parameter the applies published, so deploy, status, invite,
// snapshot, sleep and wake need no Terraform and no checkout.
func (o Ops) install(ctx context.Context) (map[string]string, error) {
	raw, err := o.AWS.In(o.installRegion()).GetParameter(ctx, o.installParam(), false)
	if err != nil {
		if errors.Is(err, ErrParameterNotFound) {
			return nil, ErrNoInstall
		}
		return nil, fmt.Errorf("reading %s: %w", o.installParam(), err)
	}
	if raw == "" { // an empty parameter is no install, not corrupt JSON
		return nil, ErrNoInstall
	}
	m := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, fmt.Errorf("%s is not the JSON Crucible wrote (%w): run `crucible aws up` to rewrite it", o.installParam(), err)
	}
	return m, nil
}

// needTerraform is the gate on the four commands that create or destroy
// resources. Every other command must never call it, so an operator who only
// deploys and connects installs no Terraform at all.
func (o Ops) needTerraform() error {
	look := o.LookPath
	if look == nil {
		look = exec.LookPath
	}
	if _, err := look("terraform"); err != nil {
		return fmt.Errorf("terraform is not on your PATH, and `crucible aws init`, `labs-init`, `up` and `teardown` need it "+
			"(%s): install %s from %s. Every other command works without it",
			err, terraformVersion, terraformInstallURL)
	}
	return nil
}

// installJSON is the stored shape, exported for tests in this package.
func installJSON(m map[string]string) string {
	b, _ := json.Marshal(m)
	return string(b)
}

// execLookPath is exec.LookPath, behind a name the package can share.
func execLookPath(file string) (string, error) { return exec.LookPath(file) }
