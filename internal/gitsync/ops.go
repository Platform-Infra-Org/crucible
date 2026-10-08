package gitsync

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"crucible/internal/apperr"
)

// Op is one change in a content edit or draft: "put" writes Path with Content (creating it if needed), "rename" moves
// From to To, "delete" removes Path. However they are listed, ApplyOps runs renames first, then deletes, then puts, so a
// put can change a file renamed in the same edit.
type Op struct {
	Op      string `json:"op"`
	Path    string `json:"path,omitempty"`
	From    string `json:"from,omitempty"`
	To      string `json:"to,omitempty"`
	Content string `json:"content"` // always sent, so a put of an empty file still carries it
}

func invalidf(format string, a ...any) error {
	return apperr.Wrap(apperr.Invalid, fmt.Sprintf(format, a...))
}

// CheckPath allows exactly what an edit may touch: training.yaml or a file inside modules/<id>/ (editPath) with a text
// extension (.md/.yaml/.yml/.sh).
func CheckPath(rel string) error {
	if err := editPath(rel); err != nil {
		return apperr.Wrap(apperr.Invalid, err.Error())
	}
	if !slices.Contains(editExts, strings.ToLower(path.Ext(rel))) {
		return invalidf("%s: only .md, .yaml, .yml and .sh files can be edited here", rel)
	}
	return nil
}

// PutOps turns the pre-ops edit shape ({path: new content}) into put ops, sorted by path.
func PutOps(files map[string]string) []Op {
	ops := []Op{}
	for _, p := range slices.Sorted(maps.Keys(files)) {
		ops = append(ops, Op{Op: "put", Path: p, Content: files[p]})
	}
	return ops
}

// Targets are the paths an edit writes: puts and rename targets (what the case-collision check looks at).
func Targets(ops []Op) []string {
	var out []string
	for _, op := range ops {
		switch op.Op {
		case "put":
			out = append(out, op.Path)
		case "rename":
			out = append(out, op.To)
		}
	}
	return out
}

// CheckOps enforces what one edit may do: 1–20 ops, every path passing CheckPath, puts of at most 256 KiB of UTF-8
// text without NUL, no path named twice in the same role, nothing both renamed and deleted, and training.yaml never
// renamed or deleted.
func CheckOps(ops []Op) error {
	if len(ops) == 0 || len(ops) > maxEditFiles {
		return invalidf("an edit changes 1 to %d files", maxEditFiles)
	}
	seen := map[string]bool{}
	once := func(role, p string) error {
		if seen[role+"\x00"+p] {
			return invalidf("%s: named twice in one edit", p)
		}
		seen[role+"\x00"+p] = true
		return nil
	}
	for _, op := range ops {
		var paths []string
		var err error
		switch op.Op {
		case "put":
			if op.From != "" || op.To != "" {
				return invalidf("a put takes a path and content")
			}
			if len(op.Content) > maxEditFile || strings.ContainsRune(op.Content, 0) || !utf8.ValidString(op.Content) {
				return invalidf("%s: must be text of at most 256 KiB", op.Path)
			}
			paths, err = []string{op.Path}, once("put", op.Path)
		case "rename":
			switch {
			case op.Path != "" || op.Content != "":
				return invalidf("a rename takes from and to")
			case op.From == "training.yaml" || op.To == "training.yaml":
				return invalidf("training.yaml can't be renamed or deleted")
			case op.From == op.To:
				return invalidf("%s: renamed to itself", op.From)
			}
			if err = once("from", op.From); err == nil {
				err = once("to", op.To)
			}
			paths = []string{op.From, op.To}
		case "delete":
			switch {
			case op.From != "" || op.To != "" || op.Content != "":
				return invalidf("a delete takes only a path")
			case op.Path == "training.yaml":
				return invalidf("training.yaml can't be renamed or deleted")
			}
			paths, err = []string{op.Path}, once("delete", op.Path)
		default:
			return invalidf("unknown op %q: use put, rename or delete", op.Op)
		}
		if err != nil {
			return err
		}
		for _, p := range paths {
			if err := CheckPath(p); err != nil {
				return err
			}
		}
	}
	for _, op := range ops {
		switch {
		case op.Op == "delete" && (seen["from\x00"+op.Path] || seen["to\x00"+op.Path]):
			return invalidf("%s: renamed and deleted in one edit", op.Path)
		case op.Op == "delete" && seen["put\x00"+op.Path]:
			return invalidf("%s: deleted and put in one edit", op.Path)
		}
	}
	return nil
}

// ApplyOps applies ops (already accepted by CheckOps) to the tree at dir: renames first (every source is read before any
// target is written, so swaps work), then deletes, then puts. Nothing is written through a symlink. It returns the
// paths that are new at dir (put on a missing path, or a rename target). Existing files keep their mode; a rename target
// already has its source's mode, a new put 0644; the caller adjusts modes (e.g. the exec bit) as policy needs.
func ApplyOps(dir string, ops []Op) ([]string, error) {
	regular := func(rel string) (string, fs.FileMode, error) {
		if err := NoSymlinks(dir, rel); err != nil {
			return "", 0, invalidf("%s: %v", rel, err)
		}
		p := filepath.Join(dir, filepath.FromSlash(rel))
		fi, err := os.Lstat(p)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return "", 0, invalidf("%s: no such file", rel)
		case err != nil:
			return "", 0, err
		case !fi.Mode().IsRegular():
			return "", 0, invalidf("%s: not a regular file", rel)
		}
		return p, fi.Mode().Perm(), nil
	}
	write := func(rel string, body []byte, mode fs.FileMode) error {
		if err := NoSymlinks(dir, rel); err != nil {
			return invalidf("%s: %v", rel, err)
		}
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return invalidf("%s: %v", rel, err)
		}
		if err := os.WriteFile(p, body, mode); err != nil {
			return err
		}
		return os.Chmod(p, mode)
	}
	type move struct {
		src, to string
		body    []byte
		mode    fs.FileMode
	}
	var moves []move
	for _, op := range ops {
		if op.Op != "rename" {
			continue
		}
		p, mode, err := regular(op.From)
		if err != nil {
			return nil, err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		moves = append(moves, move{p, op.To, b, mode})
	}
	sources := map[string]bool{}
	for _, op := range ops {
		if op.Op == "rename" {
			sources[op.From] = true
		}
	}
	for _, m := range moves { // refuse a bad target before anything is removed
		if err := NoSymlinks(dir, m.to); err != nil {
			return nil, invalidf("%s: %v", m.to, err)
		}
		if _, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(m.to))); err == nil && !sources[m.to] {
			return nil, invalidf("%s: already exists", m.to)
		}
	}
	for _, m := range moves {
		if err := os.Remove(m.src); err != nil {
			return nil, err
		}
	}
	var created []string
	for _, m := range moves {
		if _, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(m.to))); err == nil {
			return nil, invalidf("%s: already exists", m.to)
		}
		if err := write(m.to, m.body, m.mode); err != nil {
			return nil, err
		}
		created = append(created, m.to)
	}
	for _, op := range ops {
		if op.Op != "delete" {
			continue
		}
		p, _, err := regular(op.Path)
		if err != nil {
			return nil, err
		}
		if err := os.Remove(p); err != nil {
			return nil, err
		}
	}
	for _, op := range ops {
		if op.Op != "put" {
			continue
		}
		if err := NoSymlinks(dir, op.Path); err != nil {
			return nil, invalidf("%s: %v", op.Path, err)
		}
		mode, isNew := fs.FileMode(0o644), true
		if fi, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(op.Path))); err == nil {
			if !fi.Mode().IsRegular() {
				return nil, invalidf("%s: not a regular file", op.Path)
			}
			mode, isNew = fi.Mode().Perm(), false
		}
		if err := write(op.Path, []byte(op.Content), mode); err != nil {
			return nil, err
		}
		if isNew && !slices.Contains(created, op.Path) {
			created = append(created, op.Path)
		}
	}
	return created, nil
}
