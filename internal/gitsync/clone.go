package gitsync

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	errRaced = errors.New("push rejected: the branch moved")
	shaRE    = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// syncClone makes dir a clean checkout of url's branch tip, cloning on first use and re-cloning a broken copy once.
func syncClone(ctx context.Context, url, branch, dir string) error {
	if strings.HasPrefix(url, "-") || strings.HasPrefix(branch, "-") {
		return fmt.Errorf("invalid repo %q or branch %q", redact(url), branch)
	}
	err := checkoutTip(ctx, url, branch, dir)
	if err != nil && ctx.Err() == nil {
		_ = os.RemoveAll(dir)
		err = checkoutTip(ctx, url, branch, dir)
	}
	return err
}

func checkoutTip(ctx context.Context, url, branch, dir string) error {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		_ = os.RemoveAll(dir)
		if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
			return err
		}
		if _, err := git(ctx, "", "clone", "-q", "--branch", branch, "--", url, dir); err != nil {
			return err
		}
	}
	if _, err := git(ctx, dir, "fetch", "-q", "origin", branch); err != nil {
		return err
	}
	if _, err := git(ctx, dir, "reset", "-q", "--hard", "FETCH_HEAD"); err != nil {
		return err
	}
	_, err := git(ctx, dir, "clean", "-qfdx")
	return err
}

// raced reports a push rejected because the remote branch moved.
func raced(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "non-fast-forward") || strings.Contains(msg, "fetch first") || strings.Contains(msg, "[rejected]")
}
