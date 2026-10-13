package gitsync

import (
	"context"
	"path/filepath"
	"testing"
)

// CheckWrite asks the host whether this credential may push, without pushing anything: the repository's refs are
// the same afterwards.
func TestCheckWriteProbesWithoutChangingTheRepository(t *testing.T) {
	ctx := context.Background()
	remote := bare(t, map[string]string{"a.txt": "one\n"})
	before := gitOut(t, remote, "for-each-ref")
	if err := CheckWrite(ctx, remote); err != nil {
		t.Fatalf("a repository this process may push to: %v", err)
	}
	if after := gitOut(t, remote, "for-each-ref"); after != before {
		t.Fatalf("the probe changed the repository's refs:\nbefore %s\nafter  %s", before, after)
	}
	if err := CheckWrite(ctx, filepath.Join(t.TempDir(), "missing.git")); err == nil {
		t.Fatal("a repository that cannot be reached was reported writable")
	}
	if err := CheckWrite(ctx, "--receive-pack=touch /tmp/x"); err == nil {
		t.Fatal("an option-looking url must be refused")
	}
}
