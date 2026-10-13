package org

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"golang.org/x/crypto/ssh"

	"crucible/internal/db/dbtest"
	"crucible/internal/secrets"
)

func repoStore(t *testing.T) *Store {
	t.Helper()
	box, err := secrets.New("a-long-enough-development-key")
	if err != nil {
		t.Fatal(err)
	}
	return &Store{DB: dbtest.New(t), Secrets: box, SSHDir: sshDir(t.TempDir())}
}

// Connecting over SSH makes the pair here: the private half is sealed and the public half, which is not a secret, is
// kept in the clear so it can be shown again.
func TestConnectOverSSHKeepsThePrivateHalfSealed(t *testing.T) {
	ctx := context.Background()
	s := repoStore(t)
	id, err := s.ConnectRepo(ctx, "admin@x", 0, RepoBody{URL: "git@github.com:org/trainings.git", Auth: "ssh"}, s.Secrets)
	if err != nil {
		t.Fatal(err)
	}
	var sealed, public string
	if err := s.DB.QueryRow(ctx, `SELECT secret, public_key FROM repositories WHERE id = $1`, id).Scan(&sealed, &public); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sealed, "v1:") || strings.Contains(sealed, "PRIVATE KEY") {
		t.Fatalf("the private key is not sealed in the database: %q", sealed)
	}
	if !strings.HasPrefix(public, "ssh-ed25519 ") {
		t.Fatalf("the public key is not stored for showing: %q", public)
	}
	opened, err := s.Secrets.Open(sealed)
	if err != nil || !strings.Contains(opened, "PRIVATE KEY") {
		t.Fatalf("the sealed value is not the private key: %v", err)
	}

	// The view may say a credential is set and show the public half, never the private one.
	repos, err := s.Repos(ctx)
	if err != nil || len(repos) != 1 {
		t.Fatalf("repos: %v %d", err, len(repos))
	}
	if !repos[0].HasSecret || repos[0].PublicKey != public {
		t.Fatalf("the view hides the public key or forgets the secret: %+v", repos[0])
	}

	// git is given an alias, so another repository on the same host can use a different key.
	url, err := s.fetchURL(ctx, id, "git@github.com:org/trainings.git", "ssh", "", sealed)
	if err != nil {
		t.Fatal(err)
	}
	if url != "ssh://git@crucible-repo-"+itoa(id)+"/org/trainings.git" {
		t.Fatalf("git would be given %q", url)
	}
}

// An audit row records that a credential was set, never the credential.
func TestConnectAuditsWithoutTheCredential(t *testing.T) {
	ctx := context.Background()
	s := repoStore(t)
	if _, err := s.ConnectRepo(ctx, "admin@x", 0, RepoBody{URL: "https://github.com/org/t.git", Auth: "token", Secret: "ghp_SuperSecret"}, s.Secrets); err != nil {
		t.Fatal(err)
	}
	var detail string
	mustScan(t, s.DB, `SELECT detail::text FROM audit_log WHERE action = 'repo.connect'`, &detail)
	if strings.Contains(detail, "SuperSecret") {
		t.Fatalf("the credential reached the audit log: %s", detail)
	}
	if !strings.Contains(detail, `"secret_set": true`) {
		t.Fatalf("the audit row does not record that one was set: %s", detail)
	}
}

func TestTokenAndKeyAreRefusedOnTheWrongKindOfURL(t *testing.T) {
	ctx := context.Background()
	s := repoStore(t)
	for name, b := range map[string]RepoBody{
		"a token over ssh":         {URL: "git@github.com:o/r.git", Auth: "token", Secret: "x"},
		"a credential in the URL":  {URL: "https://tok@github.com/o/r.git", Auth: "token", Secret: "x"},
		"a deploy key for no repo": {URL: "https://github.com", Auth: "ssh"},
	} {
		if _, err := s.ConnectRepo(ctx, "admin@x", 0, b, s.Secrets); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// Choosing a deploy key for an http(s) repository reaches the same repository over SSH: Crucible stores the git@
// form rather than making someone retype the URL.
func TestADeployKeyOverHTTPSStoresTheSSHForm(t *testing.T) {
	ctx := context.Background()
	s := repoStore(t)
	id, err := s.ConnectRepo(ctx, "admin@x", 0, RepoBody{URL: "https://github.com/org/trainings.git", Auth: "ssh"}, s.Secrets)
	if err != nil {
		t.Fatal(err)
	}
	var url, auth string
	if err := s.DB.QueryRow(ctx, `SELECT url, auth FROM repositories WHERE id = $1`, id).Scan(&url, &auth); err != nil {
		t.Fatal(err)
	}
	if url != "git@github.com:org/trainings.git" || auth != "ssh" {
		t.Fatalf("stored %q as %q", url, auth)
	}
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

// fakeSSHHost answers SSH on a local port with whichever host key it holds at the time, so a test can stand in for a
// host whose key changes. It authenticates anyone and opens no channel: reaching the key exchange is all that matters.
type fakeSSHHost struct {
	addr string
	key  atomic.Pointer[ssh.Signer]
}

func newFakeSSHHost(t *testing.T) *fakeSSHHost {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	h := &fakeSSHHost{addr: l.Addr().String()}
	h.rotate(t)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			cfg := &ssh.ServerConfig{NoClientAuth: true}
			cfg.AddHostKey(*h.key.Load())
			go func() {
				defer c.Close()
				_, chans, reqs, err := ssh.NewServerConn(c, cfg)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(reqs)
				for ch := range chans {
					_ = ch.Reject(ssh.Prohibited, "no git here")
				}
			}()
		}
	}()
	return h
}

func (h *fakeSSHHost) rotate(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	h.key.Store(&signer)
}

// The host key ssh pins on first contact is kept in Postgres, not only in the data directory a restart may wipe: a
// wiped directory gets it back before ssh runs, so a host that changed its key meanwhile is still refused rather than
// trusted afresh.
func TestPinnedHostKeySurvivesAWipedDataDir(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("no ssh client")
	}
	ctx := context.Background()
	s := repoStore(t)
	dir := string(s.SSHDir)
	cfg, err := s.UseSSHDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_SSH_COMMAND", "ssh -F "+cfg)
	host := newFakeSSHHost(t)
	id, err := s.ConnectRepo(ctx, "admin@x", 0, RepoBody{URL: "ssh://git@" + host.addr + "/org/trainings.git", Auth: "ssh"}, s.Secrets)
	if err != nil {
		t.Fatal(err)
	}
	lastError := func() string {
		var e string
		if err := s.DB.QueryRow(ctx, `SELECT last_error FROM repositories WHERE id = $1`, id).Scan(&e); err != nil {
			t.Fatal(err)
		}
		return e
	}

	s.Check(ctx, id) // first contact: ssh pins the host's key
	pinned, err := os.ReadFile(filepath.Join(dir, "known_hosts"))
	if err != nil || !strings.Contains(string(pinned), "ssh-ed25519") {
		t.Fatalf("ssh pinned nothing on first contact (%v): %s", err, lastError())
	}
	s.Check(ctx, id) // the next use saves the pin

	// A restart on an emptyDir: the data directory is gone. Meanwhile the host presents a different key.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UseSSHDir(dir); err != nil {
		t.Fatal(err)
	}
	host.rotate(t)
	s.Check(ctx, id)
	if e := lastError(); !strings.Contains(e, "Host key verification failed") {
		t.Fatalf("a changed host key after a restart was not refused; last error: %q", e)
	}
	restored, err := os.ReadFile(filepath.Join(dir, "known_hosts"))
	if err != nil || string(restored) != string(pinned) {
		t.Fatalf("known_hosts was not restored as pinned (%v):\n got %q\nwant %q", err, restored, pinned)
	}
}
