package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDestroyRejectsBadLabID(t *testing.T) {
	root := t.TempDir()
	victim := filepath.Join(root, "x")
	_ = os.MkdirAll(victim, 0o755)
	c := Compose{Dir: filepath.Join(root, "labs")}
	_ = os.MkdirAll(c.Dir, 0o755)
	if err := c.Destroy(context.Background(), "../x"); err == nil {
		t.Fatal("expected error")
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("victim removed: %v", err)
	}
}

func TestValidIDOnlyAcceptsServerLabIDs(t *testing.T) {
	for _, bad := range []string{".", "..", "", "../x", "lab1", "0123456789AB", "0123456789abc", "0123456789a/"} {
		if validID(bad) == nil {
			t.Errorf("%q must be rejected", bad)
		}
	}
	if err := validID("0123456789ab"); err != nil {
		t.Fatal(err)
	}
}

func TestLabsListsOnlyLabDirs(t *testing.T) {
	c := Compose{Dir: t.TempDir()}
	for _, d := range []string{"0123456789ab", "notalab"} {
		_ = os.MkdirAll(filepath.Join(c.Dir, d), 0o755)
	}
	if got := c.Labs(); len(got) != 1 || got[0] != "0123456789ab" {
		t.Fatalf("Labs() = %v", got)
	}
}
