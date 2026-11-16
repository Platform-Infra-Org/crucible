package awsops

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/credentials/ssocreds"
)

// ssoFor serves the Identity Center device flow and the account/role listings,
// routed by path, and returns Ops pointed at it with HOME in a temp directory
// so the token cache is written somewhere disposable.
func ssoFor(t *testing.T, h http.HandlerFunc) (Ops, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	var log bytes.Buffer
	o := Ops{Log: &log, Sleep: func(time.Duration) {}, Endpoint: srv.URL}
	t.Cleanup(func() { t.Logf("login log: %s", log.String()) })
	return o, home
}

func deviceFlow(t *testing.T, tokenAfter int, accounts, roles string) http.HandlerFunc {
	t.Helper()
	calls := 0
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/client/register"):
			_, _ = w.Write([]byte(`{"clientId":"cid","clientSecret":"csecret",` +
				`"clientSecretExpiresAt":4102444800}`))
		case strings.HasSuffix(r.URL.Path, "/device_authorization"):
			_, _ = w.Write([]byte(`{"deviceCode":"dev","userCode":"ABCD-EFGH",` +
				`"verificationUri":"https://device.sso/","verificationUriComplete":"https://device.sso/?user_code=ABCD-EFGH",` +
				`"expiresIn":600,"interval":1}`))
		case strings.HasSuffix(r.URL.Path, "/token"):
			calls++
			if calls <= tokenAfter {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"__type":"AuthorizationPendingException","error":"authorization_pending"}`))
				return
			}
			_, _ = w.Write([]byte(`{"accessToken":"tok","tokenType":"Bearer","expiresIn":28800,"refreshToken":"ref"}`))
		case strings.Contains(r.URL.Path, "/assignment/accounts"):
			_, _ = w.Write([]byte(accounts))
		case strings.Contains(r.URL.Path, "/assignment/roles"):
			_, _ = w.Write([]byte(roles))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"__type":"ResourceNotFoundException","message":"` + r.URL.Path + `"}`))
		}
	}
}

const oneAccount = `{"accountList":[{"accountId":"111122223333","accountName":"Platform"}]}`
const oneRole = `{"roleList":[{"roleName":"Workload-Admin-PS","accountId":"111122223333"}]}`

func TestLoginWritesTheSharedTokenCache(t *testing.T) {
	o, home := ssoFor(t, deviceFlow(t, 0, oneAccount, oneRole))
	var opened string
	err := o.Login(context.Background(), LoginOpts{
		StartURL:  "https://example/start",
		SSORegion: "il-central-1",
		Open:      func(u string) error { opened = u; return nil },
	})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if opened != "https://device.sso/?user_code=ABCD-EFGH" {
		t.Errorf("opened %q, want the complete verification URL", opened)
	}
	// The file the AWS CLI reads, so `aws` and `crucible` are interchangeably
	// signed in. One cache, not two.
	dir := filepath.Join(home, ".aws", "sso", "cache")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("no token cache at %s: %v", dir, err)
	}
	var cached map[string]any
	found := false
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("%s is not JSON: %v", e.Name(), err)
		}
		if m["accessToken"] == "tok" {
			cached, found = m, true
		}
		if info, err := e.Info(); err == nil && info.Mode().Perm() != 0o600 {
			t.Errorf("%s has mode %v, want 0600: it holds a bearer token", e.Name(), info.Mode().Perm())
		}
	}
	if !found {
		t.Fatalf("no cache entry holds the access token; entries: %v", entries)
	}
	if cached["startUrl"] != "https://example/start" || cached["region"] != "il-central-1" {
		t.Errorf("cache = %v, want the start URL and region the CLI records", cached)
	}
	if _, ok := cached["expiresAt"]; !ok {
		t.Error("cache has no expiresAt, so nothing can tell when the token dies")
	}
}

func TestLoginPollsUntilAuthorizationCompletes(t *testing.T) {
	// Two pending replies then a token: the operator is still clicking.
	o, _ := ssoFor(t, deviceFlow(t, 2, oneAccount, oneRole))
	err := o.Login(context.Background(), LoginOpts{
		StartURL:  "https://example/start",
		SSORegion: "il-central-1",
		Open:      func(string) error { return nil },
	})
	if err != nil {
		t.Fatalf("Login must wait for the operator, not give up: %v", err)
	}
}

func TestLoginDiscoversTheOnlyAccountAndRole(t *testing.T) {
	o, _ := ssoFor(t, deviceFlow(t, 0, oneAccount, oneRole))
	acct, role, err := o.LoginAndResolve(context.Background(), LoginOpts{
		StartURL:  "https://example/start",
		SSORegion: "il-central-1",
		Open:      func(string) error { return nil },
	})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if acct != "111122223333" || role != "Workload-Admin-PS" {
		t.Errorf("resolved %q/%q, want the single account and role", acct, role)
	}
}

func TestLoginAsksWhenSeveralAccountsMatch(t *testing.T) {
	two := `{"accountList":[{"accountId":"111122223333","accountName":"Platform"},` +
		`{"accountId":"444455556666","accountName":"Labs"}]}`
	o, _ := ssoFor(t, deviceFlow(t, 0, two, oneRole))
	_, _, err := o.LoginAndResolve(context.Background(), LoginOpts{
		StartURL:  "https://example/start",
		SSORegion: "il-central-1",
		Open:      func(string) error { return nil },
	})
	if err == nil {
		t.Fatal("two accounts must not be guessed between")
	}
	for _, want := range []string{"111122223333", "444455556666"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s, so the operator cannot choose", err, want)
		}
	}
}

func TestLoginKeepsAGivenAccountAndRole(t *testing.T) {
	// Already configured: no listing call is needed, so the handler refuses them.
	o, _ := ssoFor(t, deviceFlow(t, 0, `{"accountList":[]}`, `{"roleList":[]}`))
	acct, role, err := o.LoginAndResolve(context.Background(), LoginOpts{
		StartURL:  "https://example/start",
		SSORegion: "il-central-1",
		Account:   "999988887777",
		Role:      "Given-Role",
		Open:      func(string) error { return nil },
	})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if acct != "999988887777" || role != "Given-Role" {
		t.Errorf("resolved %q/%q, want the configured pair kept", acct, role)
	}
}

// An operator whose ~/.aws/config uses an [sso-session] block signs in once and
// must stay signed in: login writes the cache under the session's key, so the
// credential provider has to read it under the same key. Keyed differently, the
// file is there, every command misses it, and "run `crucible aws login`" repeats
// for ever.
func TestTheTokenCacheKeyMatchesWhatCredentialsRead(t *testing.T) {
	o, _ := ssoFor(t, deviceFlow(t, 0, oneAccount, oneRole))
	in := LoginOpts{
		StartURL:  "https://example/start",
		SSORegion: "il-central-1",
		Session:   "Platform-AWS",
		Account:   "111122223333",
		Role:      "Admin",
		Open:      func(string) error { return nil },
	}
	if err := o.Login(context.Background(), in); err != nil {
		t.Fatalf("Login: %v", err)
	}
	written, err := ssocreds.StandardCachedTokenFilepath(in.Session)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(written); err != nil {
		t.Fatalf("login did not write %s: %v", written, err)
	}
	// The path NewAWS's provider will look in, for the same configuration.
	read := cacheKeyFor(AWSConfig{
		SSOStartURL: in.StartURL,
		SSOSession:  in.Session,
		SSORegion:   in.SSORegion,
	})
	wantPath, err := ssocreds.StandardCachedTokenFilepath(read)
	if err != nil {
		t.Fatal(err)
	}
	if wantPath != written {
		t.Fatalf("login wrote %s but credentials read %s: the operator would be told to sign in for ever", written, wantPath)
	}
}

func TestConfigCarriesTheSessionIntoAWSConfig(t *testing.T) {
	c, err := LoadConfig(writeConfig(t, minimalConfig+
		"\naws:\n  sso_start_url: https://example/start\n  sso_region: il-central-1\n  sso_session: Platform-AWS\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.AWSConfig().SSOSession; got != "Platform-AWS" {
		t.Errorf("AWSConfig().SSOSession = %q, want the file's sso_session", got)
	}
}

// Discovering the account and role is useless unless the operator is told to
// write them down: the credential provider reads them from crucible.yaml, and
// without them every later command reports being signed out.
func TestLoginSaysWhatToPutInTheConfig(t *testing.T) {
	o, _ := ssoFor(t, deviceFlow(t, 0, oneAccount, oneRole))
	var log strings.Builder
	o.Log = &log
	if err := o.Login(context.Background(), LoginOpts{
		StartURL:  "https://example/start",
		SSORegion: "il-central-1",
		Open:      func(string) error { return nil },
	}); err != nil {
		t.Fatalf("Login: %v", err)
	}
	for _, want := range []string{"crucible.yaml", "111122223333", "Workload-Admin-PS"} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("the output must name %s:\n%s", want, log.String())
		}
	}
}
