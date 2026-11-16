package awsops

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// fakeAPI is the recorder wearing the AWS interface. It appends one line per
// API call to r.calls, spelled the way the `aws` CLI spelled the same call, and
// answers from r.output / r.outErr — so the package's existing assertions keep
// their meaning now that the calls go through the SDK instead of a subprocess.
//
// Injected error text is mapped onto the package's sentinels, because the
// production code branches on errors.Is, not on substrings.
type fakeAPI struct {
	r      *recorder
	region string
}

var _ AWS = (*fakeAPI)(nil)

func (f *fakeAPI) at() string {
	if f.region == "" {
		return "eu-west-1"
	}
	return f.region
}

func (f *fakeAPI) In(region string) AWS {
	if region == "" {
		return f
	}
	return &fakeAPI{r: f.r, region: region}
}

func (f *fakeAPI) Region() string { return f.at() }

// call records cmd and returns the recorder's answer for it, with injected
// errors mapped onto the sentinels the production code tests for.
func (f *fakeAPI) call(cmd string) (string, error) {
	f.r.calls = append(f.r.calls, cmd)
	if f.r.outErr != nil {
		if err := f.r.outErr(cmd); err != nil {
			switch {
			case strings.Contains(err.Error(), "ParameterNotFound"):
				return "", fmt.Errorf("%w: %w", ErrParameterNotFound, err)
			case strings.Contains(err.Error(), "InvalidInstanceId"), strings.Contains(err.Error(), "not registered"):
				return "", fmt.Errorf("%w: %w", ErrInstanceNotReady, err)
			}
			return "", err
		}
	}
	if f.r.output == nil {
		return "", nil
	}
	return f.r.output(cmd), nil
}

func (f *fakeAPI) Account(ctx context.Context) (string, error) {
	out, err := f.call("aws sts get-caller-identity --query Account --output text")
	return strings.TrimSpace(out), err
}

func (f *fakeAPI) GetParameter(ctx context.Context, name string, decrypt bool) (string, error) {
	out, err := f.call("aws ssm get-parameter --region " + f.at() + " --name " + name)
	return strings.TrimSpace(out), err
}

func (f *fakeAPI) PutParameter(ctx context.Context, name, value string, secure bool) error {
	t := "String"
	if secure {
		t = "SecureString"
	}
	_, err := f.call("aws ssm put-parameter --region " + f.at() + " --name " + name +
		" --type " + t + " --value " + value + " --overwrite")
	return err
}

func (f *fakeAPI) DeleteParameter(ctx context.Context, name string) error {
	_, err := f.call("aws ssm delete-parameter --region " + f.at() + " --name " + name)
	return err
}

func (f *fakeAPI) SendCommand(ctx context.Context, instanceID, document string, params map[string][]string) (string, error) {
	p, _ := json.Marshal(params)
	out, err := f.call("aws ssm send-command --region " + f.at() + " --instance-ids " + instanceID +
		" --document-name " + document + " --parameters " + string(p))
	return strings.TrimSpace(out), err
}

// CommandResult probes in three separate calls, as the CLI did: the status,
// then stdout on success or stderr on failure. The package's tests key their
// answers off those --query flags, so the probes keep that spelling.
func (f *fakeAPI) CommandResult(ctx context.Context, commandID, instanceID string) (string, string, string, error) {
	probe := func(query string) (string, error) {
		return f.call("aws ssm get-command-invocation --region " + f.at() + " --command-id " + commandID +
			" --instance-id " + instanceID + " --query " + query + " --output text")
	}
	status, err := probe("Status")
	if err != nil {
		return "", "", "", err
	}
	switch status = strings.TrimSpace(status); status {
	case "Success":
		stdout, err := probe("StandardOutputContent")
		return status, stdout, "", err
	case "Failed", "Cancelled", "TimedOut":
		stderr, err := probe("StandardErrorContent")
		return status, "", stderr, err
	}
	return status, "", "", nil
}

func (f *fakeAPI) InstanceOnline(ctx context.Context, instanceID string) (bool, error) {
	out, err := f.call("aws ssm describe-instance-information --region " + f.at() +
		" --filters Key=InstanceIds,Values=" + instanceID)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "Online", nil
}
