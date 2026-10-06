// Package awscloud is Crucible's narrow view of the shared AWS lab account (spec §8.2, §9.3): lab credentials with
// session tags, the tag inventory, deleting leftovers, CloudTrail create calls and Cost Explorer. Client talks to
// AWS; Fake keeps everything in memory for tests and for CRUCIBLE_AWS_LABS=dryrun.
package awscloud

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws/arn"
)

// Tags Crucible puts on every lab session and every lab resource (provider default_tags, IAM conditions).
const (
	TagLab      = "crucible:lab-id"
	TagTeam     = "crucible:team"
	TagTraining = "crucible:training"
)

type Session struct{ LabID, Team, Training string }

type Credentials struct {
	AccessKeyID, SecretAccessKey, SessionToken string
	Expires                                    time.Time
}

// File is the shared credentials file the workspace and the terraform pods read (AWS_SHARED_CREDENTIALS_FILE).
func (c Credentials) File() []byte {
	return []byte("[default]\naws_access_key_id = " + c.AccessKeyID + "\naws_secret_access_key = " + c.SecretAccessKey +
		"\naws_session_token = " + c.SessionToken + "\n")
}

// Resource is one tagged resource. LabID is the raw crucible:lab-id tag value: untrusted, check ValidLabID.
type Resource struct{ ARN, LabID string }

type DailyCost struct {
	Day   time.Time // UTC midnight
	LabID string
	USD   float64
}

// TrailEvent is a create call by a lab session that carried no crucible:lab-id tag (see Client.LabWrites).
type TrailEvent struct {
	ID        string
	At        time.Time
	LabID     string
	Event     string
	Resources []string
}

var ErrUnsupported = errors.New("Crucible cannot delete this resource type; delete it by hand")

type Cloud interface {
	// AssumeLab returns one-hour credentials for the lab role, tagged with the session's lab, team and training.
	AssumeLab(ctx context.Context, s Session) (Credentials, error)
	// Tagged lists resources in region tagged crucible:lab-id (= labID, or any value when labID is "").
	Tagged(ctx context.Context, region, labID string) ([]Resource, error)
	// Delete removes one resource with lab credentials. deleted is false when it was already gone;
	// ErrUnsupported for types Crucible does not delete.
	Delete(ctx context.Context, region string, c Credentials, arn string) (deleted bool, err error)
	// Costs is daily cost per lab for [from, to); untagged spend is left out.
	Costs(ctx context.Context, from, to time.Time) ([]DailyCost, error)
	// LabWrites lists successful create calls by lab sessions since `since` whose request had no lab tag.
	LabWrites(ctx context.Context, region string, since time.Time) ([]TrailEvent, error)
}

var labIDRe = regexp.MustCompile(`^[0-9a-f]{12}$`)

// ValidLabID guards every value that becomes a session tag or decides a deletion.
func ValidLabID(id string) bool { return labIDRe.MatchString(id) }

func SessionName(labID string) string { return "crucible-lab-" + labID }

// kind is the resource type Delete knows how to remove, and its id; "" when it does not.
func kind(s string) (typ, id string) {
	a, err := arn.Parse(s)
	if err != nil {
		return "", ""
	}
	switch a.Service {
	case "ec2":
		t, id, _ := strings.Cut(a.Resource, "/")
		if t == "instance" || t == "volume" || t == "security-group" {
			return t, id
		}
	case "s3":
		if a.Resource != "" && !strings.Contains(a.Resource, "/") {
			return "bucket", a.Resource
		}
	}
	return "", ""
}
