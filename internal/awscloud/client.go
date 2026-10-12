package awscloud

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
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
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	tagging "github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
	tagtypes "github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	ststypes "github.com/aws/aws-sdk-go-v2/service/sts/types"
	"github.com/aws/smithy-go"
)

type Config struct {
	LabRoleARN  string // assumed per lab, with session tags (deploy/aws/labs output lab_role_arn)
	OpsRoleARN  string // inventory, CloudTrail and the billing export in S3 (output ops_role_arn)
	PlanRoleARN string // read-only, for estimates' terraform plan (output plan_role_arn); unset = no estimates
	// The CUR 2.0 export: s3://<ExportBucket>/<ExportPrefix>/<ExportName>/data/BILLING_PERIOD=YYYY-MM/*.csv.gz
	ExportBucket, ExportPrefix, ExportName string
	Endpoint                               string // tests only: every service at this URL (S3 path-style)
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

func (c *Client) AssumePlan(ctx context.Context) (Credentials, error) {
	if c.cfg.PlanRoleARN == "" {
		return Credentials{}, errors.New("estimates need the plan role (CRUCIBLE_AWS_PLAN_ROLE_ARN)")
	}
	out, err := c.sts().AssumeRole(ctx, &sts.AssumeRoleInput{RoleArn: aws.String(c.cfg.PlanRoleARN),
		RoleSessionName: aws.String("crucible-plan"), DurationSeconds: aws.Int32(900)})
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
			if id, ok := bucketLab(r.ARN); ok {
				r.LabID = id
			}
			if labID == "" || r.LabID == labID {
				out = append(out, r)
			}
		}
	}
	return c.labBuckets(ctx, region, labID, out)
}

// labBuckets adds the region's lab buckets found by name to res: an untagged (or retagged) bucket is swept too.
func (c *Client) labBuckets(ctx context.Context, region, labID string, res []Resource) ([]Resource, error) {
	cl := s3.NewFromConfig(c.ops, func(o *s3.Options) {
		o.Region, o.BaseEndpoint, o.UsePathStyle = region, c.endpoint(), c.cfg.Endpoint != ""
	})
	seen := map[string]bool{}
	for _, r := range res {
		seen[r.ARN] = true
	}
	p := s3.NewListBucketsPaginator(cl, &s3.ListBucketsInput{Prefix: aws.String(BucketPrefix + labID), BucketRegion: aws.String(region)})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, b := range page.Buckets {
			arn := "arn:aws:s3:::" + aws.ToString(b.Name)
			if id, _ := bucketLab(arn); !seen[arn] {
				seen[arn] = true
				res = append(res, Resource{ARN: arn, LabID: id})
			}
		}
	}
	return res, nil
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
				err = terminate(ctx, e, id)
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

// terminate turns termination protection off first when the lab set it (allowed by the labs stack's
// UnprotectOwnTermination, for the lab's own instances only).
func terminate(ctx context.Context, e *ec2.Client, id string) error {
	in := &ec2.TerminateInstancesInput{InstanceIds: []string{id}}
	_, err := e.TerminateInstances(ctx, in)
	var api smithy.APIError
	if !errors.As(err, &api) || api.ErrorCode() != "OperationNotPermitted" {
		return err
	}
	if _, err := e.ModifyInstanceAttribute(ctx, &ec2.ModifyInstanceAttributeInput{InstanceId: aws.String(id),
		DisableApiTermination: &ec2types.AttributeBooleanValue{Value: aws.Bool(false)}}); err != nil {
		return err
	}
	_, err = e.TerminateInstances(ctx, in)
	return err
}

// notYet are the error codes of a dependency that goes away by itself (an instance terminating, a writer finishing).
var notYet = map[string]bool{"DependencyViolation": true, "VolumeInUse": true, "InvalidVolume.InUse": true,
	"IncorrectState": true, "IncorrectInstanceState": true, "BucketNotEmpty": true}

// deleteBucket aborts unfinished multipart uploads, removes every object version and delete marker, then the bucket.
// A per-object delete error is returned as that object's API error (DeleteObjects answers 200 with Errors).
func deleteBucket(ctx context.Context, cl *s3.Client, bucket string) error {
	up := &s3.ListMultipartUploadsInput{Bucket: aws.String(bucket)}
	for {
		out, err := cl.ListMultipartUploads(ctx, up)
		if err != nil {
			return err
		}
		for _, u := range out.Uploads {
			if _, err := cl.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{Bucket: aws.String(bucket), Key: u.Key,
				UploadId: u.UploadId}); err != nil {
				return err
			}
		}
		if !aws.ToBool(out.IsTruncated) {
			break
		}
		up.KeyMarker, up.UploadIdMarker = out.NextKeyMarker, out.NextUploadIdMarker
	}
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
			del, err := cl.DeleteObjects(ctx, &s3.DeleteObjectsInput{Bucket: aws.String(bucket),
				Delete: &s3types.Delete{Objects: ids, Quiet: aws.Bool(true)}})
			if err != nil {
				return err
			}
			if len(del.Errors) > 0 {
				e := del.Errors[0]
				return fmt.Errorf("deleting %s (%d object(s) failed): %w", aws.ToString(e.Key), len(del.Errors),
					&smithy.GenericAPIError{Code: aws.ToString(e.Code), Message: aws.ToString(e.Message)})
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

// maxState bounds one lab's Terraform state.
const maxState = 32 << 20

func (c *Client) ReadState(ctx context.Context, region, bucket, key string) ([]byte, error) {
	cl := s3.NewFromConfig(c.ops, func(o *s3.Options) {
		o.Region, o.BaseEndpoint, o.UsePathStyle = region, c.endpoint(), c.cfg.Endpoint != ""
	})
	obj, err := cl.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		return nil, err
	}
	defer obj.Body.Close()
	b, err := io.ReadAll(io.LimitReader(obj.Body, maxState+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxState {
		return nil, fmt.Errorf("state %s is over 32 MiB", key)
	}
	return b, nil
}

// maxExportObject bounds one export file after decompression.
const maxExportObject = 512 << 20

// Costs reads the billed spend from the free CUR 2.0 export in S3 (gzipped CSV; per billing month the window touches, the files its manifest lists), summed per lab and UTC day over [from, to). Untagged lines are skipped. Any unreadable file or
// malformed cell fails the whole read: a half-read bill must never settle a lab. No files yet is no costs.
func (c *Client) Costs(ctx context.Context, from, to time.Time) ([]DailyCost, error) {
	cl := s3.NewFromConfig(c.ops, func(o *s3.Options) { o.BaseEndpoint, o.UsePathStyle = c.endpoint(), c.cfg.Endpoint != "" })
	type key struct {
		day time.Time
		lab string
	}
	sums := map[key]float64{}
	for m := time.Date(from.Year(), from.Month(), 1, 0, 0, 0, 0, time.UTC); m.Before(to); m = m.AddDate(0, 1, 0) {
		keys, err := c.manifestFiles(ctx, cl, m)
		if err != nil {
			return nil, err
		}
		for _, k := range keys {
			if err := readExportObject(ctx, cl, c.cfg.ExportBucket, k, from, to, func(d time.Time, lab string, usd float64) {
				sums[key{d, lab}] += usd
			}); err != nil {
				return nil, fmt.Errorf("export file %s: %w", k, err)
			}
		}
	}
	out := make([]DailyCost, 0, len(sums))
	for k, usd := range sums {
		out = append(out, DailyCost{Day: k.day, LabID: k.lab, USD: usd})
	}
	slices.SortFunc(out, func(a, b DailyCost) int {
		if d := a.Day.Compare(b.Day); d != 0 {
			return d
		}
		return strings.Compare(a.LabID, b.LabID)
	})
	return out, nil
}

// manifestFiles returns the keys of the month's current data files, from the export's manifest (the data prefix
// also holds files of earlier runs). No manifest: the export has not run for that month yet.
func (c *Client) manifestFiles(ctx context.Context, cl *s3.Client, m time.Time) ([]string, error) {
	base := strings.TrimPrefix(strings.Trim(c.cfg.ExportPrefix, "/")+"/"+c.cfg.ExportName, "/")
	key := base + "/metadata/BILLING_PERIOD=" + m.Format("2006-01") + "/" + c.cfg.ExportName + "-Manifest.json"
	obj, err := cl.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(c.cfg.ExportBucket), Key: aws.String(key)})
	var nk *s3types.NoSuchKey
	if errors.As(err, &nk) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer obj.Body.Close()
	var man struct {
		DataFiles []string `json:"dataFiles"`
	}
	if err := json.NewDecoder(io.LimitReader(obj.Body, 16<<20)).Decode(&man); err != nil {
		return nil, fmt.Errorf("manifest %s: %w", key, err)
	}
	pre := "s3://" + c.cfg.ExportBucket + "/"
	keys := make([]string, 0, len(man.DataFiles))
	for _, f := range man.DataFiles {
		k, ok := strings.CutPrefix(f, pre)
		if !ok {
			return nil, fmt.Errorf("manifest %s lists %q outside bucket %s", key, f, c.cfg.ExportBucket)
		}
		keys = append(keys, k)
	}
	return keys, nil
}

// labTagKeys are the spellings of the lab tag in CUR 2.0's resource_tags JSON.
var labTagKeys = []string{"user_" + TagLab, "user_crucible_lab_id"}

func readExportObject(ctx context.Context, cl *s3.Client, bucket, key string, from, to time.Time, add func(time.Time, string, float64)) error {
	obj, err := cl.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		return err
	}
	defer obj.Body.Close()
	zr, err := gzip.NewReader(obj.Body)
	if err != nil {
		return err
	}
	lr := &io.LimitedReader{R: zr, N: maxExportObject + 1}
	r := csv.NewReader(lr)
	r.FieldsPerRecord = -1
	head, err := r.Read()
	if err != nil {
		return err
	}
	col := map[string]int{}
	for i, h := range head {
		col[h] = i
	}
	di, ok1 := col["line_item_usage_start_date"]
	ci, ok2 := col["line_item_unblended_cost"]
	ti, ok3 := col["resource_tags"]
	if !ok1 || !ok2 || !ok3 {
		return errors.New("missing line_item_usage_start_date, line_item_unblended_cost or resource_tags column")
	}
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if lr.N <= 0 {
			return errors.New("file larger than 512 MiB uncompressed")
		}
		if len(rec) <= max(di, ci, ti) {
			return errors.New("short row")
		}
		var tags map[string]string
		if t := rec[ti]; t != "" && json.Unmarshal([]byte(t), &tags) != nil {
			return fmt.Errorf("bad resource_tags %q", rec[ti])
		}
		lab := ""
		for _, k := range labTagKeys {
			if lab = tags[k]; lab != "" {
				break
			}
		}
		if lab == "" {
			continue // untagged spend: not a lab's
		}
		if len(rec[di]) < 10 {
			return fmt.Errorf("bad usage date %q", rec[di])
		}
		d, err := time.Parse(time.DateOnly, rec[di][:10])
		if err != nil {
			return err
		}
		usd, err := strconv.ParseFloat(rec[ci], 64)
		if err != nil || math.IsNaN(usd) || math.IsInf(usd, 0) {
			return fmt.Errorf("bad cost %q", rec[ci])
		}
		if !d.Before(from) && d.Before(to) {
			add(d, lab, usd)
		}
	}
	return nil
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
			return out, nil
		}
		in.NextToken = res.NextToken
	}
	return out, ErrTruncated
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
