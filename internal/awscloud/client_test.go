package awscloud

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
)

type call struct {
	target, method, path, query, body string
	form                              url.Values
}

const assumeXML = `<AssumeRoleResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><AssumeRoleResult><Credentials>
<AccessKeyId>ASIA%s</AccessKeyId><SecretAccessKey>secret</SecretAccessKey><SessionToken>token-%s</SessionToken>
<Expiration>2026-10-05T10:00:00Z</Expiration></Credentials><AssumedRoleUser><Arn>arn:aws:sts::1:assumed-role/r/s</Arn>
<AssumedRoleId>AROA:s</AssumedRoleId></AssumedRoleUser></AssumeRoleResult></AssumeRoleResponse>`

// fakeAWS answers every service on one httptest server. The ops role is assumed transparently; reply answers the rest.
func fakeAWS(t *testing.T, reply func(c call) (code int, contentType, body string)) (*Client, func() []call) {
	t.Helper()
	// never real AWS: no instance metadata, no profile files; the node credentials below are static and fake
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_CONFIG_FILE", "/nonexistent")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", "/nonexistent")
	var mu sync.Mutex
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		c := call{target: r.Header.Get("X-Amz-Target"), method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, body: string(b)}
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
			c.form, _ = url.ParseQuery(string(b))
		}
		mu.Lock()
		calls = append(calls, c)
		mu.Unlock()
		if c.form.Get("Action") == "AssumeRole" {
			w.Header().Set("Content-Type", "text/xml")
			name := c.form.Get("RoleSessionName")
			_, _ = io.WriteString(w, strings.ReplaceAll(assumeXML, "%s", name))
			return
		}
		code, ct, body := reply(c)
		w.Header().Set("Content-Type", ct)
		w.WriteHeader(code)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	node := aws.Config{Region: "eu-west-1", RetryMaxAttempts: 1,
		Credentials: credentials.NewStaticCredentialsProvider("AKIDNODE", "secret", "")}
	cl := newClient(node, Config{LabRoleARN: "arn:aws:iam::444455556666:role/crucible-lab",
		OpsRoleARN: "arn:aws:iam::444455556666:role/crucible-lab-ops", Endpoint: srv.URL})
	return cl, func() []call { mu.Lock(); defer mu.Unlock(); return append([]call(nil), calls...) }
}

func TestAssumeLabTagsTheSession(t *testing.T) {
	cl, calls := fakeAWS(t, func(call) (int, string, string) { return 500, "text/plain", "" })
	c, err := cl.AssumeLab(context.Background(), Session{LabID: "aaaaaaaaaaaa", Team: "forge", Training: "forge-401"})
	if err != nil || c.AccessKeyID != "ASIAcrucible-lab-aaaaaaaaaaaa" || !c.Expires.Equal(time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("credentials: %+v %v", c, err)
	}
	f := calls()[0].form
	want := map[string]string{"RoleArn": "arn:aws:iam::444455556666:role/crucible-lab", "RoleSessionName": "crucible-lab-aaaaaaaaaaaa",
		"DurationSeconds": "3600", "Tags.member.1.Key": "crucible:lab-id", "Tags.member.1.Value": "aaaaaaaaaaaa",
		"Tags.member.2.Key": "crucible:team", "Tags.member.2.Value": "forge", "Tags.member.3.Key": "crucible:training"}
	for k, v := range want {
		if f.Get(k) != v {
			t.Fatalf("%s = %q, want %q (form %v)", k, f.Get(k), v, f)
		}
	}
	if _, err := cl.AssumeLab(context.Background(), Session{LabID: "AAAA; Deny"}); err == nil || len(calls()) != 1 {
		t.Fatal("a malformed lab id never reaches STS")
	}
}

func TestTaggedUsesTheOpsRoleAndFiltersByLab(t *testing.T) {
	cl, calls := fakeAWS(t, func(c call) (int, string, string) {
		if c.method == "GET" && c.path == "/" { // S3 ListBuckets
			return 200, "application/xml", `<ListAllMyBucketsResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Buckets>
				<Bucket><Name>crucible-lab-aaaaaaaaaaaa</Name><BucketRegion>eu-west-1</BucketRegion></Bucket>
				<Bucket><Name>crucible-lab-aaaaaaaaaaaa-untagged</Name><BucketRegion>eu-west-1</BucketRegion></Bucket></Buckets></ListAllMyBucketsResult>`
		}
		if c.target != "ResourceGroupsTaggingAPI_20170126.GetResources" {
			return 400, "text/plain", "unexpected " + c.target
		}
		return 200, "application/x-amz-json-1.1", `{"PaginationToken":"","ResourceTagMappingList":[
			{"ResourceARN":"arn:aws:s3:::crucible-lab-aaaaaaaaaaaa","Tags":[{"Key":"crucible:lab-id","Value":"aaaaaaaaaaaa"},{"Key":"app","Value":"x"}]},
			{"ResourceARN":"arn:aws:s3:::crucible-lab-bbbbbbbbbbbb","Tags":[{"Key":"crucible:lab-id","Value":"aaaaaaaaaaaa"}]}]}`
	})
	got, err := cl.Tagged(context.Background(), "eu-west-1", "aaaaaaaaaaaa")
	// lab b tagged its bucket with a's id: by name it is b's, so a's sweep skips it; a's untagged bucket is found by name
	if err != nil || len(got) != 2 || got[0].LabID != "aaaaaaaaaaaa" || got[0].ARN != "arn:aws:s3:::crucible-lab-aaaaaaaaaaaa" ||
		got[1] != (Resource{ARN: "arn:aws:s3:::crucible-lab-aaaaaaaaaaaa-untagged", LabID: "aaaaaaaaaaaa"}) {
		t.Fatalf("tagged: %+v %v", got, err)
	}
	if last := calls()[len(calls())-1]; !strings.Contains(last.query, "prefix=crucible-lab-aaaaaaaaaaaa") || !strings.Contains(last.query, "bucket-region=eu-west-1") {
		t.Fatalf("lab buckets are listed by name prefix in the region: %+v", last)
	}
	all := calls()
	if all[0].form.Get("RoleSessionName") != "crucible-ops" || all[0].form.Get("RoleArn") != "arn:aws:iam::444455556666:role/crucible-lab-ops" {
		t.Fatalf("inventory runs as the ops role: %+v", all[0])
	}
	var in struct {
		TagFilters []struct {
			Key    string
			Values []string
		}
	}
	if err := json.Unmarshal([]byte(all[1].body), &in); err != nil || in.TagFilters[0].Key != "crucible:lab-id" || in.TagFilters[0].Values[0] != "aaaaaaaaaaaa" {
		t.Fatalf("tag filter: %s", all[1].body)
	}
}

func TestDeleteKnowsItsTypes(t *testing.T) {
	ec2XML := func(op, inner string) string {
		return `<` + op + `Response xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><requestId>r</requestId>` + inner + `</` + op + `Response>`
	}
	cl, calls := fakeAWS(t, func(c call) (int, string, string) {
		switch {
		case c.form.Get("Action") == "DescribeInstances":
			return 200, "text/xml", ec2XML("DescribeInstances", `<reservationSet><item><instancesSet><item><instanceId>i-1</instanceId><instanceState><code>16</code><name>running</name></instanceState></item></instancesSet></item></reservationSet>`)
		case c.form.Get("Action") == "TerminateInstances":
			return 200, "text/xml", ec2XML("TerminateInstances", `<instancesSet/>`)
		case c.form.Get("Action") == "DeleteVolume":
			return 400, "text/xml", `<Response><Errors><Error><Code>InvalidVolume.NotFound</Code><Message>gone</Message></Error></Errors><RequestID>r</RequestID></Response>`
		case c.method == "GET" && strings.Contains(c.query, "versions"):
			return 200, "application/xml", `<ListVersionsResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Name>crucible-lab-aaaaaaaaaaaa</Name><IsTruncated>false</IsTruncated><Version><Key>forged.txt</Key><VersionId>v1</VersionId><IsLatest>true</IsLatest></Version></ListVersionsResult>`
		case c.method == "POST" && strings.Contains(c.query, "delete"):
			return 200, "application/xml", `<DeleteResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"></DeleteResult>`
		case c.method == "DELETE":
			return 204, "application/xml", ""
		}
		return 400, "text/plain", "unexpected"
	})
	ctx, creds := context.Background(), Credentials{AccessKeyID: "ASIA", SecretAccessKey: "s", SessionToken: "t"}
	if ok, err := cl.Delete(ctx, "eu-west-1", creds, "arn:aws:ec2:eu-west-1:1:instance/i-1"); !ok || err != nil {
		t.Fatalf("running instance: %v %v", ok, err)
	}
	if ok, err := cl.Delete(ctx, "eu-west-1", creds, "arn:aws:ec2:eu-west-1:1:volume/vol-1"); ok || err != nil {
		t.Fatalf("a volume that is already gone is (false, nil): %v %v", ok, err)
	}
	n := len(calls())
	if _, err := cl.Delete(ctx, "eu-west-1", creds, "arn:aws:rds:eu-west-1:1:db:x"); !errors.Is(err, ErrUnsupported) || len(calls()) != n {
		t.Fatalf("unknown types make no call: %v", err)
	}
	if ok, err := cl.Delete(ctx, "eu-west-1", creds, "arn:aws:s3:::crucible-lab-aaaaaaaaaaaa"); !ok || err != nil {
		t.Fatalf("bucket: %v %v", ok, err)
	}
	var methods []string
	for _, c := range calls()[n:] {
		methods = append(methods, c.method)
	}
	if strings.Join(methods, " ") != "GET POST DELETE" {
		t.Fatalf("a bucket is emptied (every version) before it is deleted: %v", methods)
	}
}

func TestCostsGroupsByLabAndSkipsUntaggedSpend(t *testing.T) {
	cl, calls := fakeAWS(t, func(c call) (int, string, string) {
		return 200, "application/x-amz-json-1.1", `{"ResultsByTime":[{"TimePeriod":{"Start":"2026-10-04","End":"2026-10-05"},"Groups":[
			{"Keys":["crucible:lab-id$aaaaaaaaaaaa"],"Metrics":{"UnblendedCost":{"Amount":"0.4213","Unit":"USD"}}},
			{"Keys":["crucible:lab-id$"],"Metrics":{"UnblendedCost":{"Amount":"12.5","Unit":"USD"}}}],"Estimated":true}]}`
	})
	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	got, err := cl.Costs(context.Background(), from, from.AddDate(0, 0, 5))
	if err != nil || len(got) != 1 || got[0].LabID != "aaaaaaaaaaaa" || got[0].USD != 0.4213 || !got[0].Day.Equal(from.AddDate(0, 0, 3)) {
		t.Fatalf("costs: %+v %v", got, err)
	}
	last := calls()[len(calls())-1]
	for _, want := range []string{`"Granularity":"DAILY"`, `"Key":"crucible:lab-id"`, `"Type":"TAG"`, `"Start":"2026-10-01"`, `"End":"2026-10-06"`, `"UnblendedCost"`} {
		if last.target != "AWSInsightsIndexService.GetCostAndUsage" || !strings.Contains(last.body, want) {
			t.Fatalf("missing %s in %s %s", want, last.target, last.body)
		}
	}
}

func TestLabWritesKeepsUntaggedCreatesOnly(t *testing.T) {
	ev := func(id, user, name, raw string) map[string]any {
		return map[string]any{"EventId": id, "EventName": name, "EventTime": 1.7596e9, "Username": user, "CloudTrailEvent": raw,
			"Resources": []map[string]string{{"ResourceType": "AWS::EC2::Volume", "ResourceName": "vol-" + id}}}
	}
	body, _ := json.Marshal(map[string]any{"Events": []any{
		ev("1", "crucible-lab-aaaaaaaaaaaa", "CreateVolume", `{"requestParameters":{"size":1}}`), // kept
		ev("2", "crucible-lab-aaaaaaaaaaaa", "RunInstances", `{"requestParameters":{"tagSpecificationSet":{"items":[{"tags":[{"key":"crucible:lab-id"}]}]}}}`),
		ev("3", "crucible-lab-aaaaaaaaaaaa", "CreateVolume", `{"errorCode":"Client.UnauthorizedOperation","requestParameters":{}}`),
		ev("4", "crucible-lab-aaaaaaaaaaaa", "CreateBucket", `{"requestParameters":{"bucketName":"crucible-lab-aaaaaaaaaaaa"}}`),
		ev("5", "crucible-lab-aaaaaaaaaaaa", "CreateTags", `{"requestParameters":{}}`),
		ev("6", "alice", "CreateVolume", `{"requestParameters":{}}`),
		ev("7", "crucible-lab-aaaaaaaaaaaa", "DeleteVolume", `{"requestParameters":{}}`),
	}})
	cl, calls := fakeAWS(t, func(c call) (int, string, string) { return 200, "application/x-amz-json-1.1", string(body) })
	got, err := cl.LabWrites(context.Background(), "eu-west-1", time.Now().Add(-24*time.Hour))
	if err != nil || len(got) != 1 || got[0].ID != "1" || got[0].LabID != "aaaaaaaaaaaa" || got[0].Resources[0] != "vol-1" {
		t.Fatalf("only the successful untagged create by a lab session: %+v %v", got, err)
	}
	if last := calls()[len(calls())-1]; last.target != "CloudTrail_20131101.LookupEvents" || !strings.Contains(last.body, `"AttributeKey":"ReadOnly"`) {
		t.Fatalf("lookup: %+v", last)
	}
}

func TestDeleteTurnsTerminationProtectionOff(t *testing.T) {
	protected := true
	cl, calls := fakeAWS(t, func(c call) (int, string, string) {
		switch c.form.Get("Action") {
		case "DescribeInstances":
			return 200, "text/xml", `<DescribeInstancesResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><reservationSet><item><instancesSet><item><instanceId>i-1</instanceId><instanceState><code>16</code><name>running</name></instanceState></item></instancesSet></item></reservationSet></DescribeInstancesResponse>`
		case "TerminateInstances":
			if protected {
				return 400, "text/xml", `<Response><Errors><Error><Code>OperationNotPermitted</Code><Message>disableApiTermination</Message></Error></Errors><RequestID>r</RequestID></Response>`
			}
			return 200, "text/xml", `<TerminateInstancesResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><instancesSet/></TerminateInstancesResponse>`
		case "ModifyInstanceAttribute":
			if c.form.Get("DisableApiTermination.Value") == "false" {
				protected = false
			}
			return 200, "text/xml", `<ModifyInstanceAttributeResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><return>true</return></ModifyInstanceAttributeResponse>`
		}
		return 400, "text/plain", "unexpected"
	})
	creds := Credentials{AccessKeyID: "ASIA", SecretAccessKey: "s", SessionToken: "t"}
	if ok, err := cl.Delete(context.Background(), "eu-west-1", creds, "arn:aws:ec2:eu-west-1:1:instance/i-1"); !ok || err != nil {
		t.Fatalf("a protected instance is unprotected, then terminated: %v %v", ok, err)
	}
	var actions []string
	for _, c := range calls() {
		actions = append(actions, c.form.Get("Action"))
	}
	if got := strings.Join(actions, " "); got != "DescribeInstances TerminateInstances ModifyInstanceAttribute TerminateInstances" {
		t.Fatalf("calls: %s", got)
	}
}

func TestDeleteOfAResourceStillInUseIsNotYet(t *testing.T) {
	cl, _ := fakeAWS(t, func(c call) (int, string, string) {
		return 400, "text/xml", `<Response><Errors><Error><Code>VolumeInUse</Code><Message>vol-1 is attached to i-1</Message></Error></Errors><RequestID>r</RequestID></Response>`
	})
	creds := Credentials{AccessKeyID: "ASIA", SecretAccessKey: "s", SessionToken: "t"}
	if ok, err := cl.Delete(context.Background(), "eu-west-1", creds, "arn:aws:ec2:eu-west-1:1:volume/vol-1"); ok || !errors.Is(err, ErrNotYet) {
		t.Fatalf("an attached volume is retried later, not a failure: %v %v", ok, err)
	}
}

func TestLabWritesSaysWhenItStoppedEarly(t *testing.T) {
	body := `{"NextToken":"more","Events":[{"EventId":"1","EventName":"CreateVolume","EventTime":1.7596e9,"Username":"crucible-lab-aaaaaaaaaaaa","CloudTrailEvent":"{\"requestParameters\":{}}"}]}`
	cl, calls := fakeAWS(t, func(c call) (int, string, string) { return 200, "application/x-amz-json-1.1", body })
	got, err := cl.LabWrites(context.Background(), "eu-west-1", time.Now().Add(-24*time.Hour))
	lookups := 0
	for _, c := range calls() {
		if c.target == "CloudTrail_20131101.LookupEvents" {
			lookups++
		}
	}
	if !errors.Is(err, ErrTruncated) || len(got) != 20 || lookups != 20 {
		t.Fatalf("20 pages kept, then ErrTruncated: %d events, %d lookups, %v", len(got), lookups, err)
	}
}
