package org

import (
	"os"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestKeyPairIsUsableAndPublicHalfIsNotTheSecret(t *testing.T) {
	priv, pub, err := newKeyPair("crucible-https://git/x.git")
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.ParsePrivateKey([]byte(priv))
	if err != nil {
		t.Fatalf("the private key ssh would read: %v", err)
	}
	if !strings.HasPrefix(pub, "ssh-ed25519 ") {
		t.Fatalf("the public key is not in authorized_keys form: %q", pub)
	}
	// What is shown must match the key that is kept, or the deploy key would authorise nothing.
	want := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	if !strings.HasPrefix(pub, want) {
		t.Fatalf("the public key shown is not this pair's:\n shown %q\n pair  %q", pub, want)
	}
	if strings.Contains(pub, "PRIVATE") {
		t.Fatal("the private key leaked into the public half")
	}
}

func TestParseSSHTakesBothForms(t *testing.T) {
	for raw, want := range map[string]sshTarget{
		"git@github.com:org/repo.git":              {user: "git", host: "github.com", path: "org/repo.git"},
		"ssh://git@github.com/org/repo.git":        {user: "git", host: "github.com", path: "org/repo.git"},
		"ssh://git@git.example.com:2222/o/r.git":   {user: "git", host: "git.example.com", port: "2222", path: "o/r.git"},
		"ssh://example.com/o/r.git":                {user: "git", host: "example.com", path: "o/r.git"},
		"forge@gitlab.example.com:group/sub/x.git": {user: "forge", host: "gitlab.example.com", path: "group/sub/x.git"},
	} {
		got, err := parseSSH(raw)
		if err != nil || got != want {
			t.Errorf("%s: got %+v, %v; want %+v", raw, got, err, want)
		}
	}
	for _, bad := range []string{"https://github.com/o/r.git", "github.com/o/r.git", "git@github.com", "@host:p"} {
		if _, err := parseSSH(bad); err == nil {
			t.Errorf("%q parsed as an SSH repository", bad)
		}
	}
}

// Two repositories on one host must not share a key: each gets a Host alias of its own, and the URL git is given
// names the alias rather than the host.
func TestEachRepositoryGetsItsOwnAliasAndKey(t *testing.T) {
	d := sshDir(t.TempDir())
	one, err := d.write(1, "KEY-ONE", "git@github.com:org/a.git")
	if err != nil {
		t.Fatal(err)
	}
	two, err := d.write(2, "KEY-TWO", "ssh://git@github.com/org/b.git")
	if err != nil {
		t.Fatal(err)
	}
	if one != "ssh://git@crucible-repo-1/org/a.git" || two != "ssh://git@crucible-repo-2/org/b.git" {
		t.Fatalf("URLs do not name their alias: %q %q", one, two)
	}
	cfg, err := os.ReadFile(d.path("config"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Host crucible-repo-1", "Host crucible-repo-2", "HostName github.com",
		"IdentitiesOnly yes", "StrictHostKeyChecking accept-new", "BatchMode yes"} {
		if !strings.Contains(string(cfg), want) {
			t.Errorf("the ssh config lacks %q:\n%s", want, cfg)
		}
	}
	// The key is the repository's own and readable only by this process.
	body, err := os.ReadFile(d.path("crucible-repo-1"))
	if err != nil || string(body) != "KEY-ONE" {
		t.Fatalf("key one: %q %v", body, err)
	}
	info, err := os.Stat(d.path("crucible-repo-2"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("key two is %v, want 0600 (%v)", info.Mode().Perm(), err)
	}
}

func TestForgetRemovesTheKeyAndItsEntry(t *testing.T) {
	d := sshDir(t.TempDir())
	if _, err := d.write(7, "KEY", "git@github.com:org/a.git"); err != nil {
		t.Fatal(err)
	}
	if err := d.forget(7); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(d.path("crucible-repo-7")); !os.IsNotExist(err) {
		t.Fatal("the key outlived the connection")
	}
	cfg, _ := os.ReadFile(d.path("config"))
	if strings.Contains(string(cfg), "crucible-repo-7") {
		t.Fatalf("the ssh config still names it:\n%s", cfg)
	}
}

func TestIsSSH(t *testing.T) {
	for raw, want := range map[string]bool{
		"git@github.com:o/r.git":         true,
		"ssh://git@github.com/o/r.git":   true,
		"https://github.com/o/r.git":     false,
		"http://github.com/o/r.git":      false,
		"file:///git/forge-101.git":      false,
		"https://tok@github.com/o/r.git": false,
	} {
		if got := IsSSH(raw); got != want {
			t.Errorf("IsSSH(%q) = %v", raw, got)
		}
	}
}
