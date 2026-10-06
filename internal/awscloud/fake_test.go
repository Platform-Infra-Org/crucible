package awscloud

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestFakeBehavesLikeTheLabAccount(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	f := &Fake{Now: func() time.Time { return now }}
	if _, err := f.AssumeLab(ctx, Session{LabID: "../etc"}); err == nil {
		t.Fatal("a malformed lab id must never become a session tag")
	}
	a, _ := f.AssumeLab(ctx, Session{LabID: "aaaaaaaaaaaa", Team: "forge"})
	b, _ := f.AssumeLab(ctx, Session{LabID: "bbbbbbbbbbbb"})
	if !a.Expires.Equal(now.Add(time.Hour)) || !strings.Contains(string(a.File()), "aws_session_token = ") {
		t.Fatalf("one-hour credentials as a shared credentials file: %+v\n%s", a, a.File())
	}
	f.SimulateApply("eu-west-1", Session{LabID: "aaaaaaaaaaaa"})
	got, _ := f.Tagged(ctx, "eu-west-1", "aaaaaaaaaaaa")
	if len(got) != 2 {
		t.Fatalf("apply leaves the lab bucket and a hand-made volume: %+v", got)
	}
	if other, _ := f.Tagged(ctx, "us-east-1", ""); len(other) != 0 {
		t.Fatalf("inventory is per region: %+v", other)
	}
	vol := "arn:aws:ec2:eu-west-1:000000000000:volume/vol-aaaaaaaaaaaa"
	if _, err := f.Delete(ctx, "eu-west-1", b, vol); err == nil {
		t.Fatal("like IAM, another lab's session cannot delete this lab's resources")
	}
	f.SimulateDestroy("aaaaaaaaaaaa")
	if f.Has("arn:aws:s3:::crucible-lab-aaaaaaaaaaaa") || !f.Has(vol) {
		t.Fatal("terraform destroy removes the bucket it manages, not the hand-made volume")
	}
	if ok, err := f.Delete(ctx, "eu-west-1", a, vol); !ok || err != nil || f.Has(vol) {
		t.Fatalf("the lab's own session deletes it: %v %v", ok, err)
	}
	if ok, err := f.Delete(ctx, "eu-west-1", a, vol); ok || err != nil {
		t.Fatalf("an already deleted resource is (false, nil): %v %v", ok, err)
	}
	if _, err := f.Delete(ctx, "eu-west-1", a, "arn:aws:rds:eu-west-1:1:db:x"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unknown types are reported, not guessed at: %v", err)
	}
	if c, _ := f.Costs(ctx, now.Add(-24*time.Hour), now.Add(24*time.Hour)); len(c) != 1 || c[0].USD != 0.11 {
		t.Fatalf("apply books a small actual cost for the Ledger: %+v", c)
	}
	f.Err = errors.New("throttled")
	if _, err := f.Costs(ctx, now, now); err == nil {
		t.Fatal("Err makes every call fail")
	}
}

func TestFakeNotYet(t *testing.T) {
	ctx := context.Background()
	f := &Fake{}
	f.SimulateApply("eu-west-1", Session{LabID: "aaaaaaaaaaaa"})
	c, _ := f.AssumeLab(ctx, Session{LabID: "aaaaaaaaaaaa"})
	vol := "arn:aws:ec2:eu-west-1:000000000000:volume/vol-aaaaaaaaaaaa"
	f.NotYet(vol, 1)
	if ok, err := f.Delete(ctx, "eu-west-1", c, vol); ok || !errors.Is(err, ErrNotYet) || !f.Has(vol) {
		t.Fatalf("refused once: %v %v", ok, err)
	}
	if ok, err := f.Delete(ctx, "eu-west-1", c, vol); !ok || err != nil {
		t.Fatalf("then deleted: %v %v", ok, err)
	}
}
