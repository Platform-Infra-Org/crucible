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
