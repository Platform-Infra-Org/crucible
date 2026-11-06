package awscloud

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestConsoleURL(t *testing.T) {
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		_, _ = w.Write([]byte(`{"SigninToken":"tok123"}`))
	}))
	defer srv.Close()
	c := Credentials{AccessKeyID: "ASIA1", SecretAccessKey: "sk", SessionToken: "st"}
	u, err := ConsoleURL(context.Background(), srv.Client(), srv.URL, c, "https://crucible.example.com", "https://il-central-1.console.aws.amazon.com/ec2/home?region=il-central-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Get("Action") != "getSigninToken" || !strings.Contains(got.Get("Session"), `"sessionId":"ASIA1"`) || !strings.Contains(got.Get("Session"), `"sessionToken":"st"`) {
		t.Fatalf("token request: %v", got)
	}
	p, _ := url.Parse(u)
	q := p.Query()
	if q.Get("Action") != "login" || q.Get("SigninToken") != "tok123" || q.Get("Issuer") != "https://crucible.example.com" ||
		!strings.HasPrefix(q.Get("Destination"), "https://il-central-1.console.aws.amazon.com/ec2/") {
		t.Fatalf("login URL: %s", u)
	}
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", http.StatusForbidden) })
	if _, err := ConsoleURL(context.Background(), srv.Client(), srv.URL, c, "i", "d"); err == nil || strings.Contains(err.Error(), "sk") {
		t.Fatalf("a refused token is an error that never echoes the secret: %v", err)
	}
}

func TestConsoleURLTransportErrorNeverEchoesTheSecret(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	c := Credentials{AccessKeyID: "ASIA1", SecretAccessKey: "sk-SECRET-123", SessionToken: "st-SECRET-456"}
	hc, ep := srv.Client(), srv.URL
	srv.Close()
	_, err := ConsoleURL(context.Background(), hc, ep, c, "i", "d")
	if err == nil || strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "Session") {
		t.Fatalf("a transport error must not carry the request URL: %v", err)
	}
}
