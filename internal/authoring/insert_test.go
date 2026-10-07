package authoring

import (
	"context"
	"errors"
	"strings"
	"testing"

	"crucible/internal/apperr"
	"crucible/internal/gitsync"
)

func TestInsertReturnsOpsForTheDraft(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	res, err := f.s.Insert(ctx, f.leader, InsertReq{Training: "t1", BaseSHA: f.head(), Block: "reading", Values: map[string]string{"module": "m1", "name": "tongs", "title": "Tongs"}})
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{}
	for _, op := range res.Ops {
		paths = append(paths, op.Path)
	}
	if strings.Join(paths, " ") != "modules/m1/module.yaml modules/m1/reading/tongs.md" || res.Open != "modules/m1/reading/tongs.md" {
		t.Fatalf("result: %+v", res)
	}
	if probs, err := f.s.Validate(ctx, f.leader, ValidateReq{Training: "t1", BaseSHA: f.head(), Ops: res.Ops}); err != nil || len(probs) != 0 {
		t.Fatalf("the insert is valid: %v %v", probs, err)
	}
	if _, err := f.s.Insert(ctx, f.trainee, InsertReq{Training: "t1", BaseSHA: f.head(), Block: "reading"}); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("enrolled: %v", err)
	}
	if _, err := f.s.Blocks(f.trainee, "t1"); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("blocks for the enrolled: %v", err)
	}
}

// Review Focus 4: the author's draft has broken YAML in the target file.
func TestInsertIntoBrokenFile(t *testing.T) {
	f := setup(t)
	_, err := f.s.Insert(context.Background(), f.leader, InsertReq{Training: "t1", BaseSHA: f.head(),
		Ops:   []gitsync.Op{{Op: "put", Path: "modules/m1/module.yaml", Content: "title: M1\nitems:\n  - reading: [\n"}},
		Block: "reading", Values: map[string]string{"module": "m1", "name": "tongs", "title": "Tongs"}})
	if !errors.Is(err, apperr.Invalid) || !strings.Contains(err.Error(), "fix it first") || !strings.Contains(err.Error(), "modules/m1/module.yaml") {
		t.Fatalf("broken target: %v", err)
	}
}
