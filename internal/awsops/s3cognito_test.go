package awsops

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// httpFor is a client against a bare handler, for S3's REST protocol.
func httpFor(t *testing.T, h http.HandlerFunc) AWS {
	t.Helper()
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIATEST")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	a, err := NewAWS(context.Background(), AWSConfig{Region: "il-central-1", Endpoint: srv.URL})
	if err != nil {
		t.Fatalf("NewAWS: %v", err)
	}
	return a
}

func TestHeadObjectTellsPresentFromAbsent(t *testing.T) {
	// backupState's restore path must tell "no backup, this is a new install"
	// from "cannot tell", so the two must not collapse into one error.
	a := httpFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bkt/there" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	ok, err := a.HeadObject(context.Background(), "bkt", "there")
	if err != nil || !ok {
		t.Errorf("HeadObject(there) = %v, %v; want true, nil", ok, err)
	}
	ok, err = a.HeadObject(context.Background(), "bkt", "missing")
	if err != nil || ok {
		t.Errorf("HeadObject(missing) = %v, %v; want false, nil", ok, err)
	}
}

func TestHeadObjectReportsAccessDenied(t *testing.T) {
	// Review Focus 2: access denied is never "no backup". It must be an error.
	a := httpFor(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	if _, err := a.HeadObject(context.Background(), "bkt", "k"); err == nil {
		t.Fatal("HeadObject on a forbidden bucket returned no error: a denied read must never read as absent")
	}
}

func TestGetAndPutObjectRoundTripAFile(t *testing.T) {
	store := map[string][]byte{}
	a := httpFor(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			b := make([]byte, r.ContentLength)
			_, _ = r.Body.Read(b)
			store[r.URL.Path] = b
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			b, ok := store[r.URL.Path]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write(b)
		}
	})
	dir := t.TempDir()
	src := filepath.Join(dir, "state")
	if err := os.WriteFile(src, []byte(`{"serial":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.PutObject(context.Background(), "bkt", "persistent.tfstate", src); err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	dst := filepath.Join(dir, "restored")
	if err := a.GetObject(context.Background(), "bkt", "persistent.tfstate", dst); err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"serial":1}` {
		t.Errorf("restored %q, want the bytes that were uploaded", got)
	}
}

func TestCreateUserAlwaysVerifiesTheEmail(t *testing.T) {
	// The documented guarantee of `crucible aws invite`.
	var sent map[string]any
	a := awsFor(t, map[string]func([]byte) (int, string){
		"AdminCreateUser": func(b []byte) (int, string) {
			_ = json.Unmarshal(b, &sent)
			return 200, `{"User":{"Username":"someone@example.com"}}`
		},
	})
	if err := a.CreateUser(context.Background(), "il-central-1_abc", "someone@example.com", "A Name"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	attrs, _ := sent["UserAttributes"].([]any)
	verified := false
	for _, raw := range attrs {
		kv, _ := raw.(map[string]any)
		if kv["Name"] == "email_verified" && kv["Value"] == "true" {
			verified = true
		}
	}
	if !verified {
		t.Errorf("AdminCreateUser attributes %v lack email_verified=true", attrs)
	}
	media, _ := sent["DesiredDeliveryMediums"].([]any)
	if len(media) != 1 || media[0] != "EMAIL" {
		t.Errorf("DesiredDeliveryMediums = %v, want [EMAIL]", media)
	}
}

func TestUserStatusNotFoundIsDistinguishable(t *testing.T) {
	// Invite branches on it to choose create versus resend.
	a := awsFor(t, map[string]func([]byte) (int, string){
		"AdminGetUser": func([]byte) (int, string) {
			return 400, `{"__type":"UserNotFoundException","message":"User does not exist."}`
		},
	})
	_, err := a.UserStatus(context.Background(), "pool", "nobody@example.com")
	if !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("UserStatus error = %v, want ErrUserNotFound", err)
	}
}

func TestUserStatusReportsTheStatus(t *testing.T) {
	a := awsFor(t, map[string]func([]byte) (int, string){
		"AdminGetUser": func([]byte) (int, string) {
			return 200, `{"Username":"a@b.c","UserStatus":"FORCE_CHANGE_PASSWORD"}`
		},
	})
	st, err := a.UserStatus(context.Background(), "pool", "a@b.c")
	if err != nil {
		t.Fatalf("UserStatus: %v", err)
	}
	if st != "FORCE_CHANGE_PASSWORD" {
		t.Errorf("UserStatus = %q, want FORCE_CHANGE_PASSWORD", st)
	}
}

func TestResendInviteUsesTheResendMessageAction(t *testing.T) {
	var sent map[string]any
	a := awsFor(t, map[string]func([]byte) (int, string){
		"AdminCreateUser": func(b []byte) (int, string) {
			_ = json.Unmarshal(b, &sent)
			return 200, `{"User":{}}`
		},
	})
	if err := a.ResendInvite(context.Background(), "pool", "a@b.c"); err != nil {
		t.Fatalf("ResendInvite: %v", err)
	}
	if sent["MessageAction"] != "RESEND" {
		t.Errorf("MessageAction = %v, want RESEND", sent["MessageAction"])
	}
}

func TestVerifyEmailSetsOnlyEmailVerified(t *testing.T) {
	var sent map[string]any
	a := awsFor(t, map[string]func([]byte) (int, string){
		"AdminUpdateUserAttributes": func(b []byte) (int, string) {
			_ = json.Unmarshal(b, &sent)
			return 200, `{}`
		},
	})
	if err := a.VerifyEmail(context.Background(), "pool", "a@b.c"); err != nil {
		t.Fatalf("VerifyEmail: %v", err)
	}
	attrs, _ := sent["UserAttributes"].([]any)
	if len(attrs) != 1 {
		t.Fatalf("UserAttributes = %v, want exactly email_verified", attrs)
	}
	kv, _ := attrs[0].(map[string]any)
	if kv["Name"] != "email_verified" || kv["Value"] != "true" {
		t.Errorf("UserAttributes[0] = %v, want email_verified=true", kv)
	}
}

func TestTaggedResourcesListsARNs(t *testing.T) {
	a := awsFor(t, map[string]func([]byte) (int, string){
		"GetResources": func([]byte) (int, string) {
			return 200, `{"ResourceTagMappingList":[{"ResourceARN":"arn:aws:ec2:il-central-1:1:instance/i-1"},` +
				`{"ResourceARN":"arn:aws:s3:::lab-bucket"}]}`
		},
	})
	got, err := a.TaggedResources(context.Background(), "crucible:lab-id")
	if err != nil {
		t.Fatalf("TaggedResources: %v", err)
	}
	if len(got) != 2 || got[0] != "arn:aws:ec2:il-central-1:1:instance/i-1" {
		t.Errorf("TaggedResources = %v, want the two ARNs", got)
	}
}
