package awsops

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// Instance is as much of an EC2 instance as awsops needs: Connect finds the
// node by its Name tag and reads where to point the browser from its
// crucible:public-url tag.
type Instance struct {
	ID        string
	State     string
	PublicURL string
}

const publicURLTag = "crucible:public-url"

func (c *awsClient) ec2() *ec2.Client {
	return ec2.NewFromConfig(c.cfg, func(o *ec2.Options) {
		if c.endpoint != "" {
			o.BaseEndpoint = aws.String(c.endpoint)
		}
	})
}

func (c *awsClient) FindInstances(ctx context.Context, nameTag string, states ...string) ([]Instance, error) {
	in := &ec2.DescribeInstancesInput{
		Filters: []ec2types.Filter{{Name: aws.String("tag:Name"), Values: []string{nameTag}}},
	}
	if len(states) > 0 {
		in.Filters = append(in.Filters, ec2types.Filter{Name: aws.String("instance-state-name"), Values: states})
	}
	out, err := c.ec2().DescribeInstances(ctx, in)
	if err != nil {
		return nil, signInError(err)
	}
	var found []Instance
	for _, r := range out.Reservations {
		for _, i := range r.Instances {
			inst := Instance{ID: aws.ToString(i.InstanceId)}
			if i.State != nil {
				inst.State = string(i.State.Name)
			}
			for _, t := range i.Tags {
				if aws.ToString(t.Key) == publicURLTag {
					inst.PublicURL = aws.ToString(t.Value)
				}
			}
			found = append(found, inst)
		}
	}
	return found, nil
}

// InstanceState is the node's own state, looked up by id rather than by tag:
// status, sleep, wake and teardown all hold the id from the stack outputs.
func (c *awsClient) InstanceState(ctx context.Context, id string) (string, error) {
	out, err := c.ec2().DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{id}})
	if err != nil {
		return "", signInError(err)
	}
	for _, r := range out.Reservations {
		for _, i := range r.Instances {
			if i.State != nil {
				return string(i.State.Name), nil
			}
		}
	}
	return "", fmt.Errorf("instance %s not found", id)
}

func (c *awsClient) Subnet(ctx context.Context, id string) (string, string, error) {
	out, err := c.ec2().DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{SubnetIds: []string{id}})
	if err != nil {
		return "", "", signInError(err)
	}
	if len(out.Subnets) == 0 {
		return "", "", fmt.Errorf("subnet %s not found", id)
	}
	return aws.ToString(out.Subnets[0].VpcId), aws.ToString(out.Subnets[0].CidrBlock), nil
}

func (c *awsClient) StartInstance(ctx context.Context, id string) error {
	_, err := c.ec2().StartInstances(ctx, &ec2.StartInstancesInput{InstanceIds: []string{id}})
	return signInError(err)
}

func (c *awsClient) StopInstance(ctx context.Context, id string) error {
	_, err := c.ec2().StopInstances(ctx, &ec2.StopInstancesInput{InstanceIds: []string{id}})
	return signInError(err)
}

// WaitInstance waits for one of "stopped" or "running", using the SDK's own
// waiters so a cancelled context ends the wait.
func (c *awsClient) WaitInstance(ctx context.Context, id, state string) error {
	in := &ec2.DescribeInstancesInput{InstanceIds: []string{id}}
	const max = 15 * time.Minute
	switch state {
	case "stopped":
		return signInError(ec2.NewInstanceStoppedWaiter(c.ec2()).Wait(ctx, in, max))
	case "running":
		return signInError(ec2.NewInstanceRunningWaiter(c.ec2()).Wait(ctx, in, max))
	default:
		return errors.New("WaitInstance: only stopped and running are waited for, not " + state)
	}
}

func (c *awsClient) SerialConsoleEnabled(ctx context.Context) (bool, error) {
	out, err := c.ec2().GetSerialConsoleAccessStatus(ctx, &ec2.GetSerialConsoleAccessStatusInput{})
	if err != nil {
		return false, signInError(err)
	}
	return aws.ToBool(out.SerialConsoleAccessEnabled), nil
}
