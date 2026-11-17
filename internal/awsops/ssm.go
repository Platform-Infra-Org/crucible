package awsops

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/aws/smithy-go"
)

// ErrParameterNotFound is a parameter that does not exist, which Deploy's
// rollback tells apart from a call that failed.
var ErrParameterNotFound = errors.New("parameter not found")

// ErrInstanceNotReady is an instance that has not registered with Systems
// Manager yet. It is worth retrying; nothing else is.
var ErrInstanceNotReady = errors.New("instance not registered with Systems Manager")

func (c *awsClient) ssm() *ssm.Client {
	return ssm.NewFromConfig(c.cfg, func(o *ssm.Options) {
		if c.endpoint != "" {
			o.BaseEndpoint = aws.String(c.endpoint)
		}
	})
}

func (c *awsClient) GetParameter(ctx context.Context, name string, decrypt bool) (string, error) {
	out, err := c.ssm().GetParameter(ctx, &ssm.GetParameterInput{
		Name:           aws.String(name),
		WithDecryption: aws.Bool(decrypt),
	})
	if err != nil {
		var missing *ssmtypes.ParameterNotFound
		if errors.As(err, &missing) {
			return "", fmt.Errorf("%w: %s", ErrParameterNotFound, name)
		}
		return "", signInError(err)
	}
	return aws.ToString(out.Parameter.Value), nil
}

func (c *awsClient) PutParameter(ctx context.Context, name, value string, secure bool) error {
	t := ssmtypes.ParameterTypeString
	if secure {
		t = ssmtypes.ParameterTypeSecureString
	}
	_, err := c.ssm().PutParameter(ctx, &ssm.PutParameterInput{
		Name:      aws.String(name),
		Value:     aws.String(value),
		Type:      t,
		Overwrite: aws.Bool(true),
	})
	return signInError(err)
}

func (c *awsClient) DeleteParameter(ctx context.Context, name string) error {
	_, err := c.ssm().DeleteParameter(ctx, &ssm.DeleteParameterInput{Name: aws.String(name)})
	if err != nil {
		var missing *ssmtypes.ParameterNotFound
		if errors.As(err, &missing) {
			return fmt.Errorf("%w: %s", ErrParameterNotFound, name)
		}
		return signInError(err)
	}
	return nil
}

func (c *awsClient) SendCommand(ctx context.Context, instanceID, document string, params map[string][]string) (string, error) {
	out, err := c.ssm().SendCommand(ctx, &ssm.SendCommandInput{
		InstanceIds:  []string{instanceID},
		DocumentName: aws.String(document),
		Parameters:   params,
	})
	if err != nil {
		if notReady(err) {
			return "", fmt.Errorf("%w: %w", ErrInstanceNotReady, err)
		}
		return "", signInError(err)
	}
	return aws.ToString(out.Command.CommandId), nil
}

// notReady is the one retryable SendCommand failure: the agent has not checked
// in yet. AccessDenied, ExpiredToken and the rest never become ready by waiting.
func notReady(err error) bool {
	var invalid *ssmtypes.InvalidInstanceId
	if errors.As(err, &invalid) {
		return true
	}
	var api smithy.APIError
	return errors.As(err, &api) && api.ErrorCode() == "InvalidInstanceId"
}

func (c *awsClient) CommandResult(ctx context.Context, commandID, instanceID string) (status, stdout, stderr string, err error) {
	out, err := c.ssm().GetCommandInvocation(ctx, &ssm.GetCommandInvocationInput{
		CommandId:  aws.String(commandID),
		InstanceId: aws.String(instanceID),
	})
	if err != nil {
		return "", "", "", signInError(err)
	}
	return string(out.Status), aws.ToString(out.StandardOutputContent), aws.ToString(out.StandardErrorContent), nil
}

func (c *awsClient) InstanceOnline(ctx context.Context, instanceID string) (bool, error) {
	out, err := c.ssm().DescribeInstanceInformation(ctx, &ssm.DescribeInstanceInformationInput{
		Filters: []ssmtypes.InstanceInformationStringFilter{
			{Key: aws.String("InstanceIds"), Values: []string{instanceID}},
		},
	})
	if err != nil {
		return false, signInError(err)
	}
	if len(out.InstanceInformationList) == 0 {
		return false, nil
	}
	return out.InstanceInformationList[0].PingStatus == ssmtypes.PingStatusOnline, nil
}

// Session is a Session Manager data channel: a WebSocket URL and the token that
// opens it. Nothing here reads it — the framed, multiplexed protocol inside it
// is implemented only by AWS's session-manager-plugin, which Crucible embeds
// (see plugin.go) and hands these three values to.
type Session struct {
	ID        string
	Token     string
	StreamURL string
}

func (c *awsClient) StartSession(ctx context.Context, target, document string, params map[string][]string) (Session, error) {
	out, err := c.ssm().StartSession(ctx, &ssm.StartSessionInput{
		Target:       aws.String(target),
		DocumentName: aws.String(document),
		Parameters:   params,
	})
	if err != nil {
		return Session{}, signInError(err)
	}
	return Session{
		ID:        aws.ToString(out.SessionId),
		Token:     aws.ToString(out.TokenValue),
		StreamURL: aws.ToString(out.StreamUrl),
	}, nil
}
