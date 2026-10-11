package gitsync

import (
	"context"
	"time"
)

// CheckRemote reports whether a repository can be reached and read with the credential built into url. It asks the
// remote to list its refs, which is the cheapest thing that exercises the network, the host key and the credential
// without fetching any objects.
//
// It does not test pushing: a safe write probe needs objects to offer, and a dry-run push against someone's
// repository is not something to do behind their back. Write access is proven the first time an approved edit
// merges, so a connection that reads but cannot write reports can-write unknown rather than a guess.
//
// The error is already redacted (see git): it can be stored and shown without leaking the credential.
func CheckRemote(ctx context.Context, url string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, err := git(ctx, "", "ls-remote", "--heads", "--", url)
	return err
}
