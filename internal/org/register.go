package org

import (
	"context"
	"fmt"
	"path"
	"strings"
	"unicode"

	"crucible/internal/apperr"
	"crucible/internal/config"
	"crucible/internal/content/blocks"
	"crucible/internal/gitsync"
)

// A training's id and title belong to its content, not to whoever registers it: Crucible reads them from the
// training.yaml at the path it was given. Registering a repository with no commits writes a starter training into it
// first, so somebody can make a repository and start editing without hand-writing a training.yaml.

// Registered says what happened, for the page to report.
type Registered struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Started bool   `json:"started"` // the repository was empty and Crucible wrote a starter training into it
}

// Register adds a training by reading its identity from repo at ref and root.
func (s *Store) Register(ctx context.Context, actor string, repoID int64, ref, root, team, tag, botName, botEmail string) (Registered, error) {
	url, shown, err := s.repoURL(ctx, repoID)
	if err != nil {
		return Registered{}, err
	}
	if ref, err = config.CleanRef(ref); err != nil {
		return Registered{}, apperr.Wrap(apperr.Invalid, err.Error())
	}
	if root, err = config.CleanPath(root); err != nil {
		return Registered{}, apperr.Wrap(apperr.Invalid, err.Error())
	}
	meta, err := gitsync.ReadMeta(ctx, url, ref, root)
	if err != nil {
		return Registered{}, err
	}
	out := Registered{ID: meta.ID, Title: meta.Title}
	if meta.Empty {
		out.ID = starterID(shown, root)
		out.Title, out.Started = starterTitle(out.ID), true
		files, err := blocks.Starter(out.ID, out.Title, actor)
		if err != nil {
			return Registered{}, err
		}
		c := &gitsync.ContentRepo{URL: url, Branch: ref, Name: botName, Email: botEmail}
		if _, err := c.WriteStarter(ctx, files, root, actor, "crucible: a training to start from"); err != nil {
			return Registered{}, err
		}
	}
	if !config.ValidTrainingID(out.ID) {
		return Registered{}, apperr.Wrap(apperr.Invalid, fmt.Sprintf("that training.yaml has the id %q, which Crucible cannot use", out.ID))
	}
	if err := s.CreateTraining(ctx, actor, out.ID, config.TrainingRef{Repo: shown, Ref: ref, Path: root}, team, tag); err != nil {
		return Registered{}, err
	}
	return out, nil
}

// repoURL returns the URL git should use for a connected repository, and the one to show or store beside it.
func (s *Store) repoURL(ctx context.Context, id int64) (forGit, shown string, err error) {
	var auth, username, sealed string
	if err := s.DB.QueryRow(ctx, `SELECT url, auth, username, secret FROM repositories WHERE id = $1`, id).
		Scan(&shown, &auth, &username, &sealed); err != nil {
		return "", "", apperr.Wrap(apperr.NotFound, "that repository is not connected")
	}
	forGit, err = s.fetchURL(ctx, id, shown, auth, username, sealed)
	return forGit, shown, err
}

// starterID names a training made in an empty repository after the folder it lives in, or after the repository
// itself when it is the whole repository.
func starterID(url, root string) string {
	base := path.Base(strings.TrimSuffix(strings.TrimSuffix(url, "/"), ".git"))
	if root != "" {
		base = path.Base(root)
	}
	if i := strings.LastIndexAny(base, ":/"); i >= 0 { // git@host:org/repo keeps the host in Base
		base = base[i+1:]
	}
	keep := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			return unicode.ToLower(r)
		}
		return '-'
	}, base)
	keep = strings.Trim(keep, "-")
	if !config.ValidTrainingID(keep) {
		return "new-training"
	}
	return keep
}

// starterTitle turns an id into something a person would write, for them to change.
func starterTitle(id string) string {
	words := strings.FieldsFunc(id, func(r rune) bool { return r == '-' || r == '_' })
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	if len(words) == 0 {
		return "New training"
	}
	return strings.Join(words, " ")
}
