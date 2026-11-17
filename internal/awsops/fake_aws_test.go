package awsops

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

func (f *fakeAPI) at() string { return f.region }

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
	if err != nil {
		return "", err
	}
	// A parameter this recorder wrote reads back, so a round trip is a round
	// trip; anything else is answered by the recorder's output hook.
	if v, ok := f.r.params[name]; ok {
		return v, nil
	}
	return strings.TrimSpace(out), nil
}

func (f *fakeAPI) PutParameter(ctx context.Context, name, value string, secure bool) error {
	t := "String"
	if secure {
		t = "SecureString"
	}
	_, err := f.call("aws ssm put-parameter --region " + f.at() + " --name " + name +
		" --type " + t + " --value " + value + " --overwrite")
	if err != nil {
		return err
	}
	if f.r.params == nil {
		f.r.params = map[string]string{}
	}
	f.r.params[name] = value
	return nil
}

func (f *fakeAPI) DeleteParameter(ctx context.Context, name string) error {
	_, err := f.call("aws ssm delete-parameter --region " + f.at() + " --name " + name)
	if err == nil {
		delete(f.r.params, name)
	}
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

func (f *fakeAPI) FindInstances(ctx context.Context, nameTag string, states ...string) ([]Instance, error) {
	out, err := f.call("aws ec2 describe-instances --region " + f.at() +
		" --filters Name=tag:Name,Values=" + nameTag + " Name=instance-state-name,Values=" + strings.Join(states, ",") +
		" --query Reservations[].Instances[].[InstanceId,Tags[?Key==`crucible:public-url`]|[0].Value] --output text")
	if err != nil {
		return nil, err
	}
	var found []Instance
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		fields := strings.Fields(line)
		inst := Instance{ID: fields[0], State: "running"}
		if len(fields) > 1 && fields[1] != "None" {
			inst.PublicURL = fields[1]
		}
		found = append(found, inst)
	}
	return found, nil
}

func (f *fakeAPI) InstanceState(ctx context.Context, id string) (string, error) {
	out, err := f.call("aws ec2 describe-instances --region " + f.at() + " --instance-ids " + id +
		" --query Reservations[0].Instances[0].State.Name --output text")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (f *fakeAPI) Subnet(ctx context.Context, id string) (string, string, error) {
	out, err := f.call("aws ec2 describe-subnets --subnet-ids " + id + " --region " + f.at() +
		" --query Subnets[0].SubnetId --output text")
	if err != nil {
		return "", "", err
	}
	// A missing subnet comes back from EC2 as an error, which the recorder
	// injects through outErr; an empty answer is not a failure here.
	if vpc := strings.TrimSpace(out); vpc != "" {
		return vpc, "10.0.0.0/24", nil
	}
	return "vpc-0fake", "10.0.0.0/24", nil
}

func (f *fakeAPI) StartInstance(ctx context.Context, id string) error {
	_, err := f.call("aws ec2 start-instances --region " + f.at() + " --instance-ids " + id)
	return err
}

func (f *fakeAPI) StopInstance(ctx context.Context, id string) error {
	_, err := f.call("aws ec2 stop-instances --region " + f.at() + " --instance-ids " + id)
	return err
}

func (f *fakeAPI) WaitInstance(ctx context.Context, id, state string) error {
	_, err := f.call("aws ec2 wait instance-" + state + " --region " + f.at() + " --instance-ids " + id)
	return err
}

func (f *fakeAPI) SerialConsoleEnabled(ctx context.Context) (bool, error) {
	out, err := f.call("aws ec2 get-serial-console-access-status --region " + f.at() +
		" --query SerialConsoleAccessEnabled --output text")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "True", nil
}

func (f *fakeAPI) HeadObject(ctx context.Context, bucket, key string) (bool, error) {
	out, err := f.call("aws s3api head-object --bucket " + bucket + " --key " + key + " --region " + f.at())
	if err != nil {
		// The recorder injects "(404)" for an absent backup, as the CLI printed it.
		if strings.Contains(err.Error(), "(404)") || strings.Contains(err.Error(), "Not Found") {
			return false, nil
		}
		return false, err
	}
	return strings.TrimSpace(out) != "absent", nil
}

func (f *fakeAPI) GetObject(ctx context.Context, bucket, key, toFile string) error {
	out, err := f.call("aws s3 cp s3://" + bucket + "/" + key + " " + toFile + " --region " + f.at() + " --only-show-errors")
	if err != nil {
		return err
	}
	if out == "" {
		out = "{}"
	}
	if err := os.MkdirAll(filepath.Dir(toFile), 0o755); err != nil {
		return err
	}
	return os.WriteFile(toFile, []byte(out), 0o600)
}

func (f *fakeAPI) PutObject(ctx context.Context, bucket, key, fromFile string) error {
	_, err := f.call("aws s3 cp " + fromFile + " s3://" + bucket + "/" + key + " --region " + f.at() + " --only-show-errors")
	return err
}

// attrsJSON is the shape the CLI was given, kept so that the package's test for
// a comma inside a name still says what it meant.
func attrsJSON(kv ...string) string {
	var a []map[string]string
	for i := 0; i < len(kv); i += 2 {
		a = append(a, map[string]string{"Name": kv[i], "Value": kv[i+1]})
	}
	b, _ := json.Marshal(a)
	return string(b)
}

func (f *fakeAPI) CreateUser(ctx context.Context, poolID, email, name string) error {
	kv := []string{"email", email, "email_verified", "true"}
	if name != "" {
		kv = append(kv, "name", name)
	}
	_, err := f.call("aws cognito-idp admin-create-user --region " + f.at() + " --user-pool-id " + poolID +
		" --username " + email + " --user-attributes " + attrsJSON(kv...) + " --desired-delivery-mediums EMAIL")
	return err
}

func (f *fakeAPI) ResendInvite(ctx context.Context, poolID, email string) error {
	_, err := f.call("aws cognito-idp admin-create-user --region " + f.at() + " --user-pool-id " + poolID +
		" --username " + email + " --message-action RESEND --desired-delivery-mediums EMAIL")
	return err
}

func (f *fakeAPI) VerifyEmail(ctx context.Context, poolID, email string) error {
	_, err := f.call("aws cognito-idp admin-update-user-attributes --region " + f.at() + " --user-pool-id " + poolID +
		" --username " + email + " --user-attributes " + attrsJSON("email_verified", "true"))
	return err
}

func (f *fakeAPI) UserStatus(ctx context.Context, poolID, email string) (string, error) {
	out, err := f.call("aws cognito-idp admin-get-user --region " + f.at() + " --user-pool-id " + poolID +
		" --username " + email + " --query UserStatus --output text")
	if err != nil {
		if strings.Contains(err.Error(), "UserNotFoundException") {
			return "", fmt.Errorf("%w: %s", ErrUserNotFound, email)
		}
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (f *fakeAPI) TaggedResources(ctx context.Context, tag string) ([]string, error) {
	out, err := f.call("aws resourcegroupstaggingapi get-resources --region " + f.at() +
		" --tag-filters Key=" + tag + " --query ResourceTagMappingList[].ResourceARN --output text")
	if err != nil {
		return nil, err
	}
	return strings.Fields(out), nil
}

// StartSession keeps the `aws ssm start-session` spelling, because that is the
// call connect makes whether the plugin is driven by the AWS CLI or by us.
func (f *fakeAPI) StartSession(ctx context.Context, target, document string, params map[string][]string) (Session, error) {
	flat := ""
	for _, k := range []string{"portNumber", "localPortNumber"} { // a stable order
		if v, ok := params[k]; ok {
			if flat != "" {
				flat += ","
			}
			flat += k + "=" + strings.Join(v, ",")
		}
	}
	_, err := f.call("aws ssm start-session --region " + f.at() + " --target " + target +
		" --document-name " + document + " --parameters " + flat)
	if err != nil {
		return Session{}, err
	}
	return Session{
		ID:        "crucible-" + target,
		Token:     "a-session-token",
		StreamURL: "wss://ssmmessages." + f.at() + ".amazonaws.com/v1/data-channel/crucible-" + target,
	}, nil
}
