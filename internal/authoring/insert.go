package authoring

import (
	"context"

	"crucible/internal/apperr"
	"crucible/internal/auth"
	"crucible/internal/content/blocks"
	"crucible/internal/edits"
	"crucible/internal/gitsync"
)

type InsertReq struct {
	Training string            `json:"training"`
	BaseSHA  string            `json:"base_sha"`
	Ops      []gitsync.Op      `json:"ops"`
	Block    string            `json:"block"`
	Values   map[string]string `json:"values"`
}

// Blocks is the catalog, for someone allowed to edit training (it holds no training content).
func (s *Service) Blocks(u *auth.User, training string) ([]blocks.Block, error) {
	if _, _, err := s.Edits.Authorize(u, training); err != nil {
		return nil, err
	}
	return blocks.Catalog, nil
}

// Insert renders a block against the draft (its base with in.Ops applied) and returns the resulting file changes as
// put ops for the editor to apply. Nothing is saved: the draft's next autosave carries them.
func (s *Service) Insert(ctx context.Context, u *auth.User, in InsertReq) (*blocks.Result, error) {
	t, err := s.base(ctx, u, in.Training, in.BaseSHA)
	if err != nil {
		return nil, err
	}
	if len(in.Ops) > 0 {
		if err := gitsync.CheckOps(in.Ops); err != nil {
			return nil, err
		}
	}
	if len(in.Values) > 30 {
		return nil, apperr.Wrap(apperr.Invalid, "too many values")
	}
	var res *blocks.Result
	var ferr error
	if err := s.bounded(ctx, u.Email, func() {
		dir, _, cleanup, err := edits.Workspace(t, in.Ops)
		if err != nil {
			ferr = err
			return
		}
		defer cleanup()
		res, ferr = blocks.Apply(dir, in.Block, in.Values)
	}); err != nil {
		return nil, err
	}
	return res, ferr
}
