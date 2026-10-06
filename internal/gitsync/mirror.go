// Package gitsync mirrors the platform and content repos and keeps an in-memory State of what is valid.
package gitsync

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Mirror is a bare `git clone --mirror` of a remote, driven through the git CLI so any host and auth method works.
type Mirror struct{ URL, Dir string }

// AllowFileTransport lets remotes use the file transport (local paths, file://): local stacks mount seeded repos at
// /git. It is off by default, so a repo URL can't read the server's own disk (other clones, mirrors). crucible-api sets
// it from CRUCIBLE_GIT_ALLOW_FILE (AllowFileFromEnv); tests that use local bare repos as remotes set it in TestMain.
var AllowFileTransport bool

// AllowFileFromEnv reports whether CRUCIBLE_GIT_ALLOW_FILE=1 asks for the file transport.
func AllowFileFromEnv(getenv func(string) string) bool {
	return getenv("CRUCIBLE_GIT_ALLOW_FILE") == "1"
}

// git runs git with no host config: only CRUCIBLE_GIT_CONFIG (the image's credential helper and safe.directory)
// applies, so a host's filters, hooksPath or fsmonitor never run in the bot's clones.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	if !AllowFileTransport {
		args = append([]string{"-c", "protocol.file.allow=never"}, args...)
	}
	global := os.Getenv("CRUCIBLE_GIT_CONFIG")
	if global == "" {
		global = os.DevNull
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.WaitDelay = 5 * time.Second // a cancelled git's helpers (remote-https, a local receive-pack) may keep its output open
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+global)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", redact(strings.Join(args, " ")), err, redact(string(bytes.TrimSpace(out))))
	}
	return strings.TrimSpace(string(out)), nil
}

var urlRE = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s'"]+`)

// redact hides credentials in URLs (https://user:token@host, ?private_token=…) so git errors can be logged and returned.
// The userinfo and every query value are replaced; a URL that doesn't parse loses everything after the scheme.
func redact(s string) string {
	return urlRE.ReplaceAllStringFunc(s, func(raw string) string {
		u, err := url.Parse(raw)
		if err != nil {
			return raw[:strings.Index(raw, "://")] + "://***"
		}
		out := u.Scheme + "://"
		if u.User != nil {
			out += "***@"
		}
		out += u.Host + u.EscapedPath()
		if u.RawQuery != "" {
			keys := slices.Sorted(maps.Keys(u.Query()))
			for i, k := range keys {
				keys[i] = url.QueryEscape(k) + "=***"
			}
			out += "?" + strings.Join(keys, "&")
		}
		return out
	})
}

func (m Mirror) Fetch(ctx context.Context) error {
	if strings.HasPrefix(m.URL, "-") {
		return fmt.Errorf("invalid repo url %q", redact(m.URL))
	}
	if _, err := os.Stat(filepath.Join(m.Dir, "HEAD")); err != nil {
		if err := os.MkdirAll(filepath.Dir(m.Dir), 0o755); err != nil {
			return err
		}
		_, err := git(ctx, "", "clone", "--mirror", "--quiet", "--", m.URL, m.Dir)
		return err
	}
	_, err := git(ctx, m.Dir, "remote", "update", "--prune")
	return err
}

func (m Mirror) Resolve(ctx context.Context, ref string) (string, error) {
	if strings.HasPrefix(ref, "-") {
		return "", fmt.Errorf("invalid ref %q", ref)
	}
	return git(ctx, m.Dir, "rev-parse", "--verify", ref+"^{commit}")
}

// Export writes the tree at sha into dest once; exports are immutable and reused.
func (m Mirror) Export(ctx context.Context, sha, dest string) error {
	if _, err := os.Stat(dest); err == nil {
		return nil
	}
	tmp := dest + ".tmp"
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return err
	}
	tarPath := tmp + ".tar"
	defer os.Remove(tarPath)
	if _, err := git(ctx, m.Dir, "archive", "--format=tar", "-o", tarPath, "--end-of-options", sha); err != nil {
		return err
	}
	if out, err := exec.CommandContext(ctx, "tar", "-xf", tarPath, "-C", tmp).CombinedOutput(); err != nil {
		return fmt.Errorf("untar %s: %w: %s", sha, err, out)
	}
	return os.Rename(tmp, dest)
}
