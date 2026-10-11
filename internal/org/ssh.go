package org

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"

	"crucible/internal/apperr"
)

// Crucible generates the keypair for a repository connected over SSH, so no private key ever travels to it: the
// person pastes only the public half into their host as a deploy key. A deploy key belongs to one repository, which
// is exactly what is wanted here, and unlike a token it does not expire.
//
// Several repositories usually live on one host with different keys, which ssh cannot tell apart by host name. Each
// connection therefore gets a Host alias of its own in a generated ssh config, and its URL is rewritten to that
// alias before git sees it.

// newKeyPair returns a sealed private key and the public key to show, in the forms ssh wants.
func newKeyPair(comment string) (pemKey, authorized string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	block, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return "", "", err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", "", err
	}
	return string(pem.EncodeToMemory(block)), strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))) + " " + comment, nil
}

// sshTarget is a repository URL split into what an ssh config entry needs.
type sshTarget struct{ user, host, port, path string }

// parseSSH accepts both forms a host offers: ssh://git@host[:port]/org/repo.git and git@host:org/repo.git.
func parseSSH(raw string) (sshTarget, error) {
	if strings.HasPrefix(strings.ToLower(raw), "ssh://") {
		u, err := parseURL(raw)
		if err != nil {
			return sshTarget{}, err
		}
		t := sshTarget{user: u.User.Username(), host: u.Hostname(), port: u.Port(), path: strings.TrimPrefix(u.Path, "/")}
		if t.user == "" {
			t.user = "git"
		}
		return t, nil
	}
	user, rest, ok := strings.Cut(raw, "@")
	if !ok {
		return sshTarget{}, apperr.Wrap(apperr.Invalid, "an SSH repository looks like git@host:org/repo.git or ssh://git@host/org/repo.git")
	}
	host, path, ok := strings.Cut(rest, ":")
	if !ok || user == "" || host == "" || path == "" {
		return sshTarget{}, apperr.Wrap(apperr.Invalid, "an SSH repository looks like git@host:org/repo.git or ssh://git@host/org/repo.git")
	}
	return sshTarget{user: user, host: host, path: path}, nil
}

func parseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, apperr.Wrap(apperr.Invalid, "this repository's URL cannot be parsed")
	}
	return u, nil
}

// ToSSH turns an http(s) repository URL into the SSH form of the same repository, so choosing a deploy key does not
// make someone retype their URL. One that is already SSH is returned unchanged.
func ToSSH(raw string) (string, error) {
	if IsSSH(raw) {
		return raw, nil
	}
	u, err := parseURL(raw)
	if err != nil {
		return "", err
	}
	if u.Host == "" || strings.Trim(u.Path, "/") == "" {
		return "", apperr.Wrap(apperr.Invalid, "this URL does not name a repository on a host")
	}
	return "git@" + u.Hostname() + ":" + strings.Trim(u.Path, "/"), nil
}

// IsSSH reports whether a URL is one Crucible would reach over SSH.
func IsSSH(raw string) bool {
	if strings.HasPrefix(strings.ToLower(raw), "ssh://") {
		return true
	}
	return !strings.Contains(raw, "://") && scpUser.MatchString(raw)
}

// sshDir holds the generated keys, the ssh config that points at them and the hosts Crucible has met. It lives under
// CRUCIBLE_DATA_DIR, so a restart keeps the host keys it pinned on first connection.
type sshDir string

func (d sshDir) path(parts ...string) string {
	return filepath.Join(append([]string{string(d)}, parts...)...)
}

// write puts the key on disk and gives this repository a Host alias of its own, so two repositories on one host can
// use different keys. It returns the URL git should be given.
func (d sshDir) write(id int64, key, raw string) (string, error) {
	t, err := parseSSH(raw)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(string(d), 0o700); err != nil {
		return "", err
	}
	alias := fmt.Sprintf("crucible-repo-%d", id)
	keyFile := d.path(alias)
	if err := writeIfChanged(keyFile, []byte(key), 0o600); err != nil {
		return "", err
	}
	port := ""
	if t.port != "" {
		port = "  Port " + t.port + "\n"
	}
	// accept-new pins a host's key the first time and refuses it if it ever changes; known_hosts is kept with the
	// data, so that pinning survives a restart.
	entry := fmt.Sprintf("Host %s\n  HostName %s\n  User %s\n%s  IdentityFile %s\n  IdentitiesOnly yes\n"+
		"  StrictHostKeyChecking accept-new\n  UserKnownHostsFile %s\n  BatchMode yes\n",
		alias, t.host, t.user, port, keyFile, d.path("known_hosts"))
	if err := writeIfChanged(d.path("config-"+alias), []byte(entry), 0o600); err != nil {
		return "", err
	}
	if err := d.rebuildConfig(); err != nil {
		return "", err
	}
	return "ssh://" + t.user + "@" + alias + "/" + t.path, nil
}

// rebuildConfig gathers the per-repository entries into the one file ssh reads.
func (d sshDir) rebuildConfig() error {
	entries, err := filepath.Glob(d.path("config-*"))
	if err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("# Written by Crucible: one Host alias per connected repository. Do not edit.\n")
	for _, e := range entries {
		body, err := os.ReadFile(e)
		if err != nil {
			return err
		}
		b.Write(body)
	}
	return writeIfChanged(d.path("config"), []byte(b.String()), 0o600)
}

// forget removes a disconnected repository's key and entry.
func (d sshDir) forget(id int64) error {
	alias := fmt.Sprintf("crucible-repo-%d", id)
	for _, f := range []string{d.path(alias), d.path("config-" + alias)} {
		if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return d.rebuildConfig()
}

func writeIfChanged(path string, body []byte, mode os.FileMode) error {
	if old, err := os.ReadFile(path); err == nil && string(old) == string(body) {
		return nil
	}
	return os.WriteFile(path, body, mode)
}

// UseSSHDir points the store at the directory that holds generated keys and the ssh config, creating it. It returns
// the config file git should be told to use.
func (s *Store) UseSSHDir(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	s.SSHDir = sshDir(dir)
	if err := s.SSHDir.rebuildConfig(); err != nil {
		return "", err
	}
	return s.SSHDir.path("config"), nil
}
