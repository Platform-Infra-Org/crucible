package gitsync

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"crucible/internal/apperr"
)

// Registering a training asks the repository who it is, rather than asking the person: the id and title come from
// the training.yaml at the path they gave. An empty repository is given a starter training instead, so somebody can
// connect a repository they have just created and start editing straight away.

// Meta is what registration needs from a repository.
type Meta struct {
	ID, Title string
	Empty     bool // the repository has no commits at all: there is nothing to read, and a starter can be written
}

// ReadMeta clones ref shallowly and reads the training.yaml under root ("" is the repository root).
func ReadMeta(ctx context.Context, url, ref, root string) (Meta, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	refs, err := git(ctx, "", "ls-remote", "--heads", "--", url)
	if err != nil {
		return Meta{}, err
	}
	if strings.TrimSpace(refs) == "" {
		return Meta{Empty: true}, nil
	}
	dir, err := os.MkdirTemp("", "crucible-register-")
	if err != nil {
		return Meta{}, err
	}
	defer os.RemoveAll(dir)
	if _, err := git(ctx, "", "clone", "-q", "--depth", "1", "--branch", ref, "--", url, dir); err != nil {
		return Meta{}, err
	}
	body, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(path.Join(root, "training.yaml"))))
	if os.IsNotExist(err) {
		where := "the root of the repository"
		if root != "" {
			where = root + "/"
		}
		return Meta{}, apperr.Wrap(apperr.NotFound, fmt.Sprintf("no training.yaml in %s on %s", where, ref))
	}
	if err != nil {
		return Meta{}, err
	}
	var t struct {
		ID    string `yaml:"id"`
		Title string `yaml:"title"`
	}
	if err := yaml.Unmarshal(body, &t); err != nil {
		return Meta{}, apperr.Wrap(apperr.Invalid, "that training.yaml is not valid YAML: "+err.Error())
	}
	if t.ID == "" || t.Title == "" {
		return Meta{}, apperr.Wrap(apperr.Invalid, "that training.yaml needs an id and a title")
	}
	return Meta{ID: t.ID, Title: t.Title}, nil
}

// WriteStarter puts files into an empty repository as its first commit on ref, so the person who connected it can
// open the editor instead of writing a training.yaml by hand. It refuses a repository that already has commits.
func (c *ContentRepo) WriteStarter(ctx context.Context, files map[string]string, root, author, msg string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	author, err := cleanEmail(author)
	if err != nil {
		return "", err
	}
	refs, err := git(ctx, "", "ls-remote", "--heads", "--", c.URL)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(refs) != "" {
		return "", apperr.Wrap(apperr.Conflict, "this repository already has commits; Crucible only writes a starter training into an empty one")
	}
	dir, err := os.MkdirTemp("", "crucible-starter-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	if _, err := git(ctx, dir, "init", "-q", "-b", c.Branch); err != nil {
		return "", err
	}
	for p, body := range files {
		full := filepath.Join(dir, filepath.FromSlash(path.Join(root, p)))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			return "", err
		}
	}
	for _, args := range [][]string{
		{"add", "-A"},
		{"-c", "user.name=" + c.Name, "-c", "user.email=" + c.Email, "commit", "-q",
			"--author=" + author + " <" + author + ">", "-m", msg},
	} {
		if _, err := git(ctx, dir, args...); err != nil {
			return "", err
		}
	}
	if _, err := git(ctx, dir, "push", "-q", "--", c.URL, "HEAD:refs/heads/"+c.Branch); err != nil {
		return "", err
	}
	return git(ctx, dir, "rev-parse", "HEAD")
}
