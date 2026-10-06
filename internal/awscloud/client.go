package awscloud

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/cloudtrail"
	cttypes "github.com/aws/aws-sdk-go-v2/service/cloudtrail/types"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	cetypes "github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	tagging "github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
	tagtypes "github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	ststypes "github.com/aws/aws-sdk-go-v2/service/sts/types"
	"github.com/aws/smithy-go"
)

type Config struct {
	LabRoleARN string // assumed per lab, with session tags (deploy/aws/labs output lab_role_arn)
	OpsRoleARN string // inventory, CloudTrail and Cost Explorer (output ops_role_arn)
	Endpoint   string // tests only: every service at this URL (S3 path-style)
}

// Client is the real lab account. Its own identity is the default credential chain: the node's instance role.
type Client struct {
	cfg  Config
	node aws.Config
	ops  aws.Config
}

var _ Cloud = (*Client)(nil)

func New(ctx context.Context, c Config) (*Client, error) {
	if c.LabRoleARN == "" || c.OpsRoleARN == "" {
		return nil, errors.New("aws labs need CRUCIBLE_AWS_LAB_ROLE_ARN and CRUCIBLE_AWS_OPS_ROLE_ARN")
	}
	node, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	return newClient(node, c), nil
}

func newClient(node aws.Config, c Config) *Client {
	cl := &Client{cfg: c, node: node}
	cl.ops = node.Copy()
	cl.ops.Credentials = aws.NewCredentialsCache(stscreds.NewAssumeRoleProvider(cl.sts(), c.OpsRoleARN,
		func(o *stscreds.AssumeRoleOptions) { o.RoleSessionName = "crucible-ops" }))
	return cl
}

func (c *Client) endpoint() *string {
	if c.cfg.Endpoint == "" {
		return nil
	}
	return aws.String(c.cfg.Endpoint)
}

func (c *Client) sts() *sts.Client {
	return sts.NewFromConfig(c.node, func(o *sts.Options) { o.BaseEndpoint = c.endpoint() })
}

func (c *Client) AssumeLab(ctx context.Context, s Session) (Credentials, error) {
	if !ValidLabID(s.LabID) {
		return Credentials{}, fmt.Errorf("invalid lab id %q", s.LabID)
	}
	tags := []ststypes.Tag{{Key: aws.String(TagLab), Value: aws.String(s.LabID)}}
	if s.Team != "" {
		tags = append(tags, ststypes.Tag{Key: aws.String(TagTeam), Value: aws.String(s.Team)})
	}
	if s.Training != "" {
		tags = append(tags, ststypes.Tag{Key: aws.String(TagTraining), Value: aws.String(s.Training)})
	}
	out, err := c.sts().AssumeRole(ctx, &sts.AssumeRoleInput{RoleArn: aws.String(c.cfg.LabRoleARN),
		RoleSessionName: aws.String(SessionName(s.LabID)), DurationSeconds: aws.Int32(3600), Tags: tags})
	if err != nil {
		return Credentials{}, err
	}
	k := out.Credentials
	return Credentials{AccessKeyID: aws.ToString(k.AccessKeyId), SecretAccessKey: aws.ToString(k.SecretAccessKey),
		SessionToken: aws.ToString(k.SessionToken), Expires: aws.ToTime(k.Expiration)}, nil
}

func (c *Client) Tagged(ctx context.Context, region, labID string) ([]Resource, error) {
	cl := tagging.NewFromConfig(c.ops, func(o *tagging.Options) { o.Region = region; o.BaseEndpoint = c.endpoint() })
	filter := tagtypes.TagFilter{Key: aws.String(TagLab)}
	if labID != "" {
		filter.Values = []string{labID}
	}
	var out []Resource
	p := tagging.NewGetResourcesPaginator(cl, &tagging.GetResourcesInput{TagFilters: []tagtypes.TagFilter{filter}})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, m := range page.ResourceTagMappingList {
			r := Resource{ARN: aws.ToString(m.ResourceARN)}
			for _, t := range m.Tags {
				if aws.ToString(t.Key) == TagLab {
					r.LabID = aws.ToString(t.Value)
				}
			}
			out = append(out, r)
		}
	}
	return out, nil
}

func (c *Client) Delete(ctx context.Context, region string, cr Credentials, s string) (bool, error) {
	typ, id := kind(s)
	if typ == "" {
		return false, ErrUnsupported
	}
	cfg := c.node.Copy()
	cfg.Region = region
	cfg.Credentials = credentials.NewStaticCredentialsProvider(cr.AccessKeyID, cr.SecretAccessKey, cr.SessionToken)
	var err error
	if typ == "bucket" {
		err = deleteBucket(ctx, s3.NewFromConfig(cfg, func(o *s3.Options) {
			o.BaseEndpoint, o.UsePathStyle = c.endpoint(), c.cfg.Endpoint != ""
		}), id)
	} else {
		e := ec2.NewFromConfig(cfg, func(o *ec2.Options) { o.BaseEndpoint = c.endpoint() })
		switch typ {
		case "instance":
			// terminated instances stay listed for about an hour: they are already gone, not deleted by us
			d, derr := e.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{id}})
			if derr == nil && len(d.Reservations) > 0 && len(d.Reservations[0].Instances) > 0 {
				if st := d.Reservations[0].Instances[0].State; st != nil && (st.Name == "terminated" || st.Name == "shutting-down") {
					return false, nil
				}
			}
			if err = derr; err == nil {
				_, err = e.TerminateInstances(ctx, &ec2.TerminateInstancesInput{InstanceIds: []string{id}})
			}
		case "volume":
			_, err = e.DeleteVolume(ctx, &ec2.DeleteVolumeInput{VolumeId: aws.String(id)})
		case "security-group":
			_, err = e.DeleteSecurityGroup(ctx, &ec2.DeleteSecurityGroupInput{GroupId: aws.String(id)})
		}
	}
	var api smithy.APIError
	if errors.As(err, &api) {
		switch code := api.ErrorCode(); {
		case strings.HasSuffix(code, ".NotFound") || code == "NoSuchBucket":
			return false, nil
		case notYet[code]:
			return false, fmt.Errorf("%w: %s", ErrNotYet, api.ErrorMessage())
		}
	}
	return err == nil, err
}

// notYet are the error codes of a dependency that goes away by itself (an instance terminating, a writer finishing).
var notYet = map[string]bool{"DependencyViolation": true, "VolumeInUse": true, "InvalidVolume.InUse": true,
	"IncorrectState": true, "IncorrectInstanceState": true, "BucketNotEmpty": true}

// deleteBucket removes every object version and delete marker, then the bucket.
func deleteBucket(ctx context.Context, cl *s3.Client, bucket string) error {
	in := &s3.ListObjectVersionsInput{Bucket: aws.String(bucket)}
	for {
		out, err := cl.ListObjectVersions(ctx, in)
		if err != nil {
			return err
		}
		var ids []s3types.ObjectIdentifier
		for _, v := range out.Versions {
			ids = append(ids, s3types.ObjectIdentifier{Key: v.Key, VersionId: v.VersionId})
		}
		for _, m := range out.DeleteMarkers {
			ids = append(ids, s3types.ObjectIdentifier{Key: m.Key, VersionId: m.VersionId})
		}
		if len(ids) > 0 {
			if _, err := cl.DeleteObjects(ctx, &s3.DeleteObjectsInput{Bucket: aws.String(bucket),
				Delete: &s3types.Delete{Objects: ids, Quiet: aws.Bool(true)}}); err != nil {
				return err
			}
		}
		if !aws.ToBool(out.IsTruncated) {
			break
		}
		in.KeyMarker, in.VersionIdMarker = out.NextKeyMarker, out.NextVersionIdMarker
	}
	_, err := cl.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(bucket)})
	return err
}

func (c *Client) Costs(ctx context.Context, from, to time.Time) ([]DailyCost, error) {
	cl := costexplorer.NewFromConfig(c.ops, func(o *costexplorer.Options) { o.Region = "us-east-1"; o.BaseEndpoint = c.endpoint() })
	in := &costexplorer.GetCostAndUsageInput{
		TimePeriod:  &cetypes.DateInterval{Start: aws.String(from.Format(time.DateOnly)), End: aws.String(to.Format(time.DateOnly))},
		Granularity: cetypes.GranularityDaily,
		Metrics:     []string{"UnblendedCost"},
		GroupBy:     []cetypes.GroupDefinition{{Type: cetypes.GroupDefinitionTypeTag, Key: aws.String(TagLab)}},
	}
	var out []DailyCost
	for {
		res, err := cl.GetCostAndUsage(ctx, in)
		if err != nil {
			return nil, err
		}
		for _, r := range res.ResultsByTime {
			day, err := time.Parse(time.DateOnly, aws.ToString(r.TimePeriod.Start))
			if err != nil {
				return nil, err
			}
			for _, g := range r.Groups {
				if len(g.Keys) == 0 {
					continue
				}
				id := strings.TrimPrefix(g.Keys[0], TagLab+"$")
				if id == "" {
					continue // untagged spend: not a lab's
				}
				usd, err := strconv.ParseFloat(aws.ToString(g.Metrics["UnblendedCost"].Amount), 64)
				if err != nil {
					return nil, err
				}
				out = append(out, DailyCost{Day: day, LabID: id, USD: usd})
			}
		}
		if res.NextPageToken == nil {
			return out, nil
		}
		in.NextPageToken = res.NextPageToken
	}
}

func (c *Client) LabWrites(ctx context.Context, region string, since time.Time) ([]TrailEvent, error) {
	cl := cloudtrail.NewFromConfig(c.ops, func(o *cloudtrail.Options) { o.Region = region; o.BaseEndpoint = c.endpoint() })
	in := &cloudtrail.LookupEventsInput{StartTime: aws.Time(since), LookupAttributes: []cttypes.LookupAttribute{
		{AttributeKey: cttypes.LookupAttributeKeyReadOnly, AttributeValue: aws.String("false")}}}
	var out []TrailEvent
	// ponytail: at most 20 pages (1000 write events per region per run); LookupEvents allows 2 calls a second.
	for page := 0; page < 20; page++ {
		res, err := cl.LookupEvents(ctx, in)
		if err != nil {
			return nil, err
		}
		for _, e := range res.Events {
			if ev, ok := untaggedCreate(e); ok {
				out = append(out, ev)
			}
		}
		if res.NextToken == nil {
			break
		}
		in.NextToken = res.NextToken
	}
	return out, nil
}

var createVerbs = []string{"Create", "Run", "Allocate", "Import", "Copy", "Register"}

// untaggedCreate keeps a successful create-like call by a lab session whose request carried no crucible:lab-id tag.
// IAM requires that tag on every create a lab role may make, so each hit is a policy gap worth a human look.
// CreateBucket (lab buckets are scoped by name, tagged right after) and CreateTags are not resource creates.
func untaggedCreate(e cttypes.Event) (TrailEvent, bool) {
	id, ok := strings.CutPrefix(aws.ToString(e.Username), "crucible-lab-")
	name := aws.ToString(e.EventName)
	if !ok || name == "CreateBucket" || name == "CreateTags" ||
		!slices.ContainsFunc(createVerbs, func(v string) bool { return strings.HasPrefix(name, v) }) {
		return TrailEvent{}, false
	}
	var raw struct {
		ErrorCode         string          `json:"errorCode"`
		RequestParameters json.RawMessage `json:"requestParameters"`
	}
	if json.Unmarshal([]byte(aws.ToString(e.CloudTrailEvent)), &raw) != nil || raw.ErrorCode != "" ||
		bytes.Contains(raw.RequestParameters, []byte(TagLab)) {
		return TrailEvent{}, false
	}
	ev := TrailEvent{ID: aws.ToString(e.EventId), At: aws.ToTime(e.EventTime), LabID: id, Event: name}
	for _, r := range e.Resources {
		ev.Resources = append(ev.Resources, aws.ToString(r.ResourceName))
	}
	return ev, true
}
