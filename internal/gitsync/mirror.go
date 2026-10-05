// Package gitsync mirrors the platform and content repos and keeps an in-memory State of what is valid.
package gitsync

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Mirror is a bare `git clone --mirror` of a remote, driven through the git CLI so any host and auth method works.
type Mirror struct{ URL, Dir string }

func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, bytes.TrimSpace(out))
	}
	return strings.TrimSpace(string(out)), nil
}

func (m Mirror) Fetch(ctx context.Context) error {
	if strings.HasPrefix(m.URL, "-") {
		return fmt.Errorf("invalid repo url %q", m.URL)
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
	if _, err := git(ctx, m.Dir, "archive", "--format=tar", "-o", tarPath, sha); err != nil {
		return err
	}
	if out, err := exec.CommandContext(ctx, "tar", "-xf", tarPath, "-C", tmp).CombinedOutput(); err != nil {
		return fmt.Errorf("untar %s: %w: %s", sha, err, out)
	}
	return os.Rename(tmp, dest)
}
