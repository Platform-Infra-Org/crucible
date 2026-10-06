package gitsync

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// NoSymlinks refuses a write through a symlink: a committer could point a config file (or a folder) outside the clone.
func NoSymlinks(dir, rel string) error {
	p := dir
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		p = filepath.Join(p, part)
		fi, err := os.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			return nil // nothing below a missing component can be a link
		}
		if err != nil {
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symlink", part)
		}
	}
	return nil
}

// cleanRel checks a repo-relative path from user input: clean, slash-separated, inside the repo and outside .git.
func cleanRel(rel string) error {
	if rel == "" || strings.ContainsAny(rel, "\\\x00") || path.IsAbs(rel) || path.Clean(rel) != rel {
		return fmt.Errorf("%q is not a clean relative path", rel)
	}
	for _, part := range strings.Split(rel, "/") {
		if part == ".." || strings.EqualFold(part, ".git") {
			return fmt.Errorf("%q is outside the editable content", rel)
		}
	}
	return nil
}
