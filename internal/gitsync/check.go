package gitsync

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

// CheckRemote reports whether a repository can be reached and read with the credential built into url. It asks the
// remote to list its refs, which is the cheapest thing that exercises the network, the host key and the credential
// without fetching any objects.
//
// The error is already redacted (see git): it can be stored and shown without leaking the credential.
func CheckRemote(ctx context.Context, url string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, err := git(ctx, "", "ls-remote", "--heads", "--", url)
	return err
}

// CheckWrite reports whether the host would accept a push with the credential git ends up using: the one built into
// url, or else the instance's own git credentials, which reach every https repository. It is a dry run of deleting a
// branch that does not exist, so nothing is sent and no ref changes; hosts check push access when the push starts
// (GitHub and GitLab refuse a read-only token or deploy key there), which is all this needs.
func CheckWrite(ctx context.Context, url string) error {
	if strings.HasPrefix(url, "-") {
		return fmt.Errorf("invalid repo url %q", redact(url))
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	dir, err := os.MkdirTemp("", "crucible-write-check-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if _, err := git(ctx, "", "init", "-q", "--bare", dir); err != nil {
		return err
	}
	_, err = git(ctx, dir, "push", "-q", "--dry-run", "--", url, ":refs/heads/crucible/write-check")
	return err
}
