package awsops

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewAWSUsesTheConfiguredSSOProfile(t *testing.T) {
	// A config that names an SSO session must not fall back to the environment:
	// the credentials come from the shared token cache, which is empty here.
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIAENVIRONMENT")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "envsecret")
	t.Setenv("HOME", t.TempDir())

	a, err := NewAWS(context.Background(), AWSConfig{
		SSOStartURL: "https://example/start",
		SSORegion:   "il-central-1",
		Account:     "111122223333",
		Role:        "Admin",
		Region:      "il-central-1",
	})
	if err != nil {
		t.Fatalf("NewAWS: %v", err)
	}
	if a.Region() != "il-central-1" {
		t.Errorf("Region() = %q, want il-central-1", a.Region())
	}
	// No token in the cache, so resolving credentials must fail as "not signed
	// in" rather than silently using AKIAENVIRONMENT.
	if _, err := a.Account(context.Background()); !errors.Is(err, ErrNotSignedIn) {
		t.Errorf("Account() error = %v, want ErrNotSignedIn", err)
	}
}

func TestNewAWSFallsBackToSharedConfig(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config")
	if err := os.WriteFile(cfg, []byte("[default]\nregion = eu-west-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", cfg)
	t.Setenv("AWS_REGION", "")

	a, err := NewAWS(context.Background(), AWSConfig{})
	if err != nil {
		t.Fatalf("NewAWS: %v", err)
	}
	if a.Region() != "eu-west-1" {
		t.Errorf("Region() = %q, want eu-west-1 from the shared config file", a.Region())
	}
}

func TestAccountReportsNotSignedIn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// STS is a query/XML protocol, not JSON.
		w.Header().Set("Content-Type", "text/xml")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`<ErrorResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">` +
			`<Error><Type>Sender</Type><Code>ExpiredToken</Code>` +
			`<Message>The security token included in the request is expired</Message></Error>` +
			`<RequestId>test</RequestId></ErrorResponse>`))
	}))
	defer srv.Close()

	// Static credentials so the request reaches the server: what is under test
	// is how the server's ExpiredTokenException is reported, not credential
	// resolution.
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIATEST")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")

	a, err := NewAWS(context.Background(), AWSConfig{
		Region:   "il-central-1",
		Endpoint: srv.URL,
	})
	if err != nil {
		t.Fatalf("NewAWS: %v", err)
	}
	_, err = a.Account(context.Background())
	if !errors.Is(err, ErrNotSignedIn) {
		t.Fatalf("Account() error = %v, want ErrNotSignedIn", err)
	}
	if !strings.Contains(err.Error(), "crucible aws login") {
		t.Errorf("Account() error = %q, want it to name `crucible aws login`", err)
	}
}

func TestAppDirHonoursOverride(t *testing.T) {
	want := filepath.Join(t.TempDir(), "elsewhere")
	t.Setenv("CRUCIBLE_HOME", want)
	got, err := AppDir()
	if err != nil {
		t.Fatalf("AppDir: %v", err)
	}
	if got != want {
		t.Errorf("AppDir() = %q, want %q", got, want)
	}

	t.Setenv("CRUCIBLE_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "cfg"))
	got, err = AppDir()
	if err != nil {
		t.Fatalf("AppDir: %v", err)
	}
	base, err := os.UserConfigDir()
	if err != nil {
		t.Fatalf("UserConfigDir: %v", err)
	}
	if got != filepath.Join(base, "crucible") {
		t.Errorf("AppDir() = %q, want %q", got, filepath.Join(base, "crucible"))
	}
}

func TestConfigDirHonoursOverride(t *testing.T) {
	want := filepath.Join(t.TempDir(), "conf")
	t.Setenv("CRUCIBLE_CONFIG_DIR", want)
	got, err := ConfigDir()
	if err != nil {
		t.Fatalf("ConfigDir: %v", err)
	}
	if got != want {
		t.Errorf("ConfigDir() = %q, want %q", got, want)
	}
}
