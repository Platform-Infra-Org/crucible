package gitsync

import (
	"errors"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"crucible/internal/apperr"
)

func TestCheckOps(t *testing.T) {
	ok := [][]Op{
		{{Op: "put", Path: "modules/m1/reading/a.md", Content: "# A\n"}},
		{{Op: "put", Path: "training.yaml", Content: "id: t1\n"}},
		{{Op: "rename", From: "modules/m1/reading/a.md", To: "modules/m2/reading/a.md"}},
		{{Op: "delete", Path: "modules/m1/lab/hints/h2.md"}},
		{{Op: "rename", From: "modules/m1/a.md", To: "modules/m1/b.md"}, {Op: "rename", From: "modules/m1/b.md", To: "modules/m1/a.md"}}, // swap
		{{Op: "rename", From: "modules/m1/a.md", To: "modules/m1/b.md"}, {Op: "put", Path: "modules/m1/b.md", Content: "x"}},
	}
	for i, ops := range ok {
		if err := CheckOps(ops); err != nil {
			t.Errorf("ok %d: %v", i, err)
		}
	}
	many := []Op{}
	for i := range 21 {
		many = append(many, Op{Op: "put", Path: "modules/m1/" + string(rune('a'+i)) + ".md"})
	}
	bad := map[string][]Op{
		"none":              {},
		"too many":          many,
		"unknown op":        {{Op: "chmod", Path: "modules/m1/a.sh"}},
		"rename training":   {{Op: "rename", From: "training.yaml", To: "modules/m1/t.yaml"}},
		"rename to train":   {{Op: "rename", From: "modules/m1/t.yaml", To: "training.yaml"}},
		"delete training":   {{Op: "delete", Path: "training.yaml"}},
		"rename to itself":  {{Op: "rename", From: "modules/m1/a.md", To: "modules/m1/a.md"}},
		"put with from":     {{Op: "put", Path: "modules/m1/a.md", From: "modules/m1/b.md"}},
		"rename w/ content": {{Op: "rename", From: "modules/m1/a.md", To: "modules/m1/b.md", Content: "x"}},
		"delete w/ content": {{Op: "delete", Path: "modules/m1/a.md", Content: "x"}},
		"from twice":        {{Op: "rename", From: "modules/m1/a.md", To: "modules/m1/b.md"}, {Op: "rename", From: "modules/m1/a.md", To: "modules/m1/c.md"}},
		"to twice":          {{Op: "rename", From: "modules/m1/a.md", To: "modules/m1/c.md"}, {Op: "rename", From: "modules/m1/b.md", To: "modules/m1/c.md"}},
		"put twice":         {{Op: "put", Path: "modules/m1/a.md"}, {Op: "put", Path: "modules/m1/a.md"}},
		"renamed+deleted":   {{Op: "rename", From: "modules/m1/a.md", To: "modules/m1/b.md"}, {Op: "delete", Path: "modules/m1/a.md"}},
		"delete then put":   {{Op: "delete", Path: "modules/m1/a.md"}, {Op: "put", Path: "modules/m1/a.md", Content: "x"}},
		"deleted target":    {{Op: "rename", From: "modules/m1/a.md", To: "modules/m1/b.md"}, {Op: "delete", Path: "modules/m1/b.md"}},
		"rename tf":         {{Op: "rename", From: "modules/m1/lab/terraform/main.tf", To: "modules/m1/lab/terraform/x.tf"}},
		"delete png":        {{Op: "delete", Path: "modules/m1/reading/a.png"}},
		"dotdot from":       {{Op: "rename", From: "modules/m1/../../x.md", To: "modules/m1/a.md"}},
		"git dir target":    {{Op: "rename", From: "modules/m1/a.yaml", To: "modules/m1/.git/config.yaml"}},
		"backslash":         {{Op: "delete", Path: `modules\m1\a.md`}},
		"unicode":           {{Op: "rename", From: "modules/m1/a.md", To: "modules/m1/ä.md"}},
		"outside modules":   {{Op: "rename", From: "modules/m1/a.md", To: "a.md"}},
		"nul":               {{Op: "put", Path: "modules/m1/a.md", Content: "a\x00b"}},
		"too big":           {{Op: "put", Path: "modules/m1/a.md", Content: strings.Repeat("x", 256<<10+1)}},
		"bad utf8":          {{Op: "put", Path: "modules/m1/a.md", Content: "\xff"}},
	}
	for name, ops := range bad {
		if err := CheckOps(ops); !errors.Is(err, apperr.Invalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// FuzzCheckOps: whatever CheckOps accepts names only plain ASCII paths inside training.yaml or modules/<id>/, with a
// text extension, and never renames or deletes training.yaml. Run longer with
// go test -run=^$ -fuzz=FuzzCheckOps -fuzztime=30s ./internal/gitsync
func FuzzCheckOps(f *testing.F) {
	for _, s := range []string{"modules/m1/a.md", "training.yaml", "../x.md", "modules/m1/.git/x.md", "modules/M1/a.md",
		"modules/m1/a.tf", `modules\m1\a.md`, "modules/m1/a.md\x00", "modules/m1/ä.md", "modules//a.md", "modules/m1/a.md/",
		"modules/m1/a..md", "modules/m1/-x.sh", "modules/m1/x .md", "/etc/passwd.md", "modules/m1/​.md"} {
		f.Add("put", s, "")
		f.Add("rename", "modules/m1/a.md", s)
		f.Add("rename", s, "modules/m1/a.md")
		f.Add("delete", s, "")
	}
	f.Fuzz(func(t *testing.T, kind, a, b string) {
		op := Op{Op: kind}
		switch kind {
		case "put":
			op.Path, op.Content = a, b
		case "rename":
			op.From, op.To = a, b
		default:
			op.Path = a
		}
		if CheckOps([]Op{op}) != nil {
			return
		}
		for _, p := range []string{op.Path, op.From, op.To} {
			if p == "" {
				continue
			}
			parts := strings.Split(p, "/")
			if p != "training.yaml" && (len(parts) < 3 || parts[0] != "modules") {
				t.Fatalf("accepted %q outside modules/<id>/", p)
			}
			for _, part := range parts {
				if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".") {
					t.Fatalf("accepted %q (empty or dot component)", p)
				}
			}
			if strings.IndexFunc(p, func(r rune) bool { return r > 126 || r < 33 || r == '\\' }) >= 0 {
				t.Fatalf("accepted %q (non-ASCII, space, control or backslash)", p)
			}
			if !slices.Contains([]string{".md", ".yaml", ".yml", ".sh"}, strings.ToLower(path.Ext(p))) {
				t.Fatalf("accepted extension of %q", p)
			}
			if kind != "put" && p == "training.yaml" {
				t.Fatalf("%s of training.yaml accepted", kind)
			}
		}
	})
}

func TestApplyOps(t *testing.T) {
	dir := t.TempDir()
	for rel, body := range map[string]string{"modules/m1/a.md": "A", "modules/m1/b.md": "B", "modules/m1/c.md": "C", "modules/m1/e.md": "E"} {
		if err := writeFile(dir, rel, body); err != nil {
			t.Fatal(err)
		}
	}
	read := func(rel string) string {
		b, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			return "<missing>"
		}
		return string(b)
	}
	created, err := ApplyOps(dir, []Op{
		{Op: "put", Path: "modules/m1/b.md", Content: "B2"}, // listed first, applied last: edits b after the swap
		{Op: "rename", From: "modules/m1/a.md", To: "modules/m1/b.md"},
		{Op: "rename", From: "modules/m1/b.md", To: "modules/m1/a.md"},
		{Op: "delete", Path: "modules/m1/c.md"},
		{Op: "put", Path: "modules/m1/new/d.md", Content: "D"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{read("modules/m1/a.md"), read("modules/m1/b.md"), read("modules/m1/c.md"), read("modules/m1/new/d.md"), read("modules/m1/e.md")}; !slices.Equal(got, []string{"B", "B2", "<missing>", "D", "E"}) {
		t.Fatalf("tree after ops: %v", got)
	}
	slices.Sort(created)
	if !slices.Equal(created, []string{"modules/m1/a.md", "modules/m1/b.md", "modules/m1/new/d.md"}) {
		t.Fatalf("created: %v", created)
	}
	for name, c := range map[string]struct {
		ops  []Op
		want string
	}{
		"missing source": {[]Op{{Op: "rename", From: "modules/m1/nope.md", To: "modules/m1/x.md"}}, "no such file"},
		"target exists":  {[]Op{{Op: "rename", From: "modules/m1/a.md", To: "modules/m1/e.md"}}, "already exists"},
		"delete missing": {[]Op{{Op: "delete", Path: "modules/m1/nope.md"}}, "no such file"},
		"put via link":   {[]Op{{Op: "put", Path: "modules/m1/link/passwd.md", Content: "x"}}, "symlink"},
		"rename to link": {[]Op{{Op: "rename", From: "modules/m1/a.md", To: "modules/m1/link/x.md"}}, "symlink"},
		"delete a dir":   {[]Op{{Op: "delete", Path: "modules/m1/new"}}, "not a regular file"},
	} {
		d := t.TempDir() // fresh tree per case: a refusal must not depend on what an earlier case did
		for _, rel := range []string{"a", "e", "new/d"} {
			if err := writeFile(d, "modules/m1/"+rel+".md", rel); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Symlink("/etc", filepath.Join(d, "modules", "m1", "link")); err != nil {
			t.Fatal(err)
		}
		_, err := ApplyOps(d, c.ops)
		if !errors.Is(err, apperr.Invalid) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
		if _, serr := os.Stat(filepath.Join(d, "modules/m1/a.md")); serr != nil && name == "rename to link" || serr != nil && name == "target exists" {
			t.Errorf("%s: refused rename removed its source", name)
		}
	}
}
