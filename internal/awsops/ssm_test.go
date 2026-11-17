package awsops

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// awsFor builds a real client pointed at an httptest server that dispatches on
// the X-Amz-Target header, so the SDK's own serialisation is exercised.
func awsFor(t *testing.T, routes map[string]func(body []byte) (int, string)) AWS {
	t.Helper()
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIATEST")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := r.Header.Get("X-Amz-Target")
		if i := strings.LastIndex(target, "."); i >= 0 {
			target = target[i+1:]
		}
		body, _ := io.ReadAll(r.Body)
		h, ok := routes[target]
		if !ok {
			w.Header().Set("Content-Type", "application/x-amz-json-1.1")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"__type":"ValidationException","message":"unrouted target ` + target + `"}`))
			return
		}
		code, payload := h(body)
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		w.WriteHeader(code)
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(srv.Close)
	a, err := NewAWS(context.Background(), AWSConfig{Region: "il-central-1", Endpoint: srv.URL})
	if err != nil {
		t.Fatalf("NewAWS: %v", err)
	}
	return a
}

func TestGetParameterRoundTrips(t *testing.T) {
	var put map[string]any
	a := awsFor(t, map[string]func([]byte) (int, string){
		"GetParameter": func([]byte) (int, string) {
			return 200, `{"Parameter":{"Name":"/crucible/release","Value":"v1.2.3"}}`
		},
		"PutParameter": func(b []byte) (int, string) {
			_ = json.Unmarshal(b, &put)
			return 200, `{"Version":1}`
		},
	})
	if err := a.PutParameter(context.Background(), "/crucible/release", "v1.2.3", false); err != nil {
		t.Fatalf("PutParameter: %v", err)
	}
	if put["Name"] != "/crucible/release" || put["Value"] != "v1.2.3" || put["Overwrite"] != true {
		t.Errorf("PutParameter sent %v, want the name, the value and Overwrite=true", put)
	}
	if put["Type"] != "String" {
		t.Errorf("PutParameter Type = %v, want String for a non-secure value", put["Type"])
	}
	got, err := a.GetParameter(context.Background(), "/crucible/release", false)
	if err != nil {
		t.Fatalf("GetParameter: %v", err)
	}
	if got != "v1.2.3" {
		t.Errorf("GetParameter = %q, want v1.2.3", got)
	}
}

func TestPutParameterSecureUsesSecureString(t *testing.T) {
	var put map[string]any
	a := awsFor(t, map[string]func([]byte) (int, string){
		"PutParameter": func(b []byte) (int, string) { _ = json.Unmarshal(b, &put); return 200, `{"Version":1}` },
	})
	if err := a.PutParameter(context.Background(), "/crucible/smtp", "hunter2", true); err != nil {
		t.Fatalf("PutParameter: %v", err)
	}
	if put["Type"] != "SecureString" {
		t.Errorf("Type = %v, want SecureString: a secret must never land in a plain parameter", put["Type"])
	}
}

func TestGetParameterNotFoundIsDistinguishable(t *testing.T) {
	// Deploy's rollback path branches on absent-versus-failed, so the two must
	// not collapse into one error.
	a := awsFor(t, map[string]func([]byte) (int, string){
		"GetParameter": func([]byte) (int, string) {
			return 400, `{"__type":"ParameterNotFound","message":"not found"}`
		},
	})
	_, err := a.GetParameter(context.Background(), "/crucible/release", false)
	if !errors.Is(err, ErrParameterNotFound) {
		t.Fatalf("GetParameter error = %v, want ErrParameterNotFound", err)
	}
	if errors.Is(err, ErrNotSignedIn) {
		t.Error("a missing parameter must not be reported as a sign-in problem")
	}
}

func TestSendCommandRetriesAnUnregisteredInstance(t *testing.T) {
	calls := 0
	a := awsFor(t, map[string]func([]byte) (int, string){
		"SendCommand": func([]byte) (int, string) {
			calls++
			if calls < 2 {
				return 400, `{"__type":"InvalidInstanceId","message":"not registered"}`
			}
			return 200, `{"Command":{"CommandId":"cmd-1"}}`
		},
	})
	// The caller owns the retry loop; the client must report the first failure
	// as a distinguishable error so that loop can tell "wait" from "give up".
	_, err := a.SendCommand(context.Background(), "i-0abc", "AWS-RunShellScript", map[string][]string{"commands": {"true"}})
	if !errors.Is(err, ErrInstanceNotReady) {
		t.Fatalf("SendCommand error = %v, want ErrInstanceNotReady", err)
	}
	id, err := a.SendCommand(context.Background(), "i-0abc", "AWS-RunShellScript", map[string][]string{"commands": {"true"}})
	if err != nil {
		t.Fatalf("SendCommand: %v", err)
	}
	if id != "cmd-1" {
		t.Errorf("SendCommand = %q, want cmd-1", id)
	}
}

func TestSendCommandAccessDeniedIsNotRetryable(t *testing.T) {
	a := awsFor(t, map[string]func([]byte) (int, string){
		"SendCommand": func([]byte) (int, string) {
			return 400, `{"__type":"AccessDeniedException","message":"no"}`
		},
	})
	_, err := a.SendCommand(context.Background(), "i-0abc", "AWS-RunShellScript", nil)
	if errors.Is(err, ErrInstanceNotReady) {
		t.Fatalf("AccessDenied must not look retryable: %v", err)
	}
}

func TestCommandResultSurfacesStderrOnFailure(t *testing.T) {
	a := awsFor(t, map[string]func([]byte) (int, string){
		"GetCommandInvocation": func([]byte) (int, string) {
			return 200, `{"Status":"Failed","StandardOutputContent":"","StandardErrorContent":"deploy.sh: boom"}`
		},
	})
	status, _, stderr, err := a.CommandResult(context.Background(), "cmd-1", "i-0abc")
	if err != nil {
		t.Fatalf("CommandResult: %v", err)
	}
	if status != "Failed" {
		t.Errorf("status = %q, want Failed", status)
	}
	if stderr != "deploy.sh: boom" {
		t.Errorf("stderr = %q, want the node's error text", stderr)
	}
}

func TestInstanceOnlineReadsThePingStatus(t *testing.T) {
	a := awsFor(t, map[string]func([]byte) (int, string){
		"DescribeInstanceInformation": func([]byte) (int, string) {
			return 200, `{"InstanceInformationList":[{"PingStatus":"Online"}]}`
		},
	})
	on, err := a.InstanceOnline(context.Background(), "i-0abc")
	if err != nil {
		t.Fatalf("InstanceOnline: %v", err)
	}
	if !on {
		t.Error("InstanceOnline = false, want true for PingStatus Online")
	}
}

func TestInstanceOnlineIsFalseWhenUnregistered(t *testing.T) {
	a := awsFor(t, map[string]func([]byte) (int, string){
		"DescribeInstanceInformation": func([]byte) (int, string) {
			return 200, `{"InstanceInformationList":[]}`
		},
	})
	on, err := a.InstanceOnline(context.Background(), "i-0abc")
	if err != nil {
		t.Fatalf("InstanceOnline: %v", err)
	}
	if on {
		t.Error("InstanceOnline = true for an empty list, want false")
	}
}

func TestInReturnsAClientForAnotherRegion(t *testing.T) {
	a := awsFor(t, nil)
	if got := a.In("eu-west-1").Region(); got != "eu-west-1" {
		t.Errorf("In(\"eu-west-1\").Region() = %q, want eu-west-1", got)
	}
	if got := a.Region(); got != "il-central-1" {
		t.Errorf("In must not mutate the original: Region() = %q", got)
	}
}
