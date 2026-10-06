package gitsync

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
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

// partRE is the charset of one path component of an edit: no leading dot (hidden files, .git, .github CI config), no
// spaces or Unicode (HFS/NTFS aliases), no trailing dot.
var partRE = regexp.MustCompile(`^[A-Za-z0-9_-]([A-Za-z0-9._-]{0,98}[A-Za-z0-9_-])?$`)

// editPath checks a repo-relative path from user input: training.yaml or a file inside modules/<module-id>/, every
// component in partRE. Anything else (CI config, hooks, root scripts) can only change in git.
func editPath(rel string) error {
	parts := strings.Split(rel, "/")
	if rel != "training.yaml" && (len(parts) < 3 || parts[0] != "modules") {
		return fmt.Errorf("%q: only training.yaml and files inside modules/<module>/ can be edited here", rel)
	}
	if len(rel) > 255 {
		return fmt.Errorf("%q: path too long", rel)
	}
	for _, part := range parts {
		if !partRE.MatchString(part) {
			return fmt.Errorf("%q: path parts use only letters, digits, '.', '_' and '-' and don't start or end with '.'", rel)
		}
	}
	return nil
}
