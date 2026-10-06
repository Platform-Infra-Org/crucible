package labs

import (
	"context"
	"time"

	"crucible/internal/auth"
)

type MyLab struct {
	ID        string     `json:"id"`
	Team      string     `json:"team"`
	Training  string     `json:"training"`
	Module    string     `json:"module"`
	Title     string     `json:"title"`
	Runtime   string     `json:"runtime"`
	State     State      `json:"state"`
	CreatedAt time.Time  `json:"created_at"`
	EndsAt    *time.Time `json:"ends_at,omitempty"`
	Link      string     `json:"link"`
}

// Mine lists the user's own labs for the Labs page: active ones first, then the newest, at most 20.
func (s *Service) Mine(ctx context.Context, u *auth.User) ([]MyLab, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+instCols+` FROM lab_instances WHERE user_id = $1
		ORDER BY (state IN ('pending_approval', 'provisioning', 'ready', 'destroying')) DESC, created_at DESC LIMIT 20`, u.ID)
	if err != nil {
		return nil, err
	}
	insts, err := collectInst(rows)
	if err != nil {
		return nil, err
	}
	out := []MyLab{}
	for _, in := range insts {
		m := MyLab{ID: in.ID, Team: in.Team, Training: in.Training, Module: in.Module, Title: in.Module, Runtime: in.Runtime,
			State: in.State, CreatedAt: in.CreatedAt, EndsAt: in.EndsAt, Link: labLink(in)}
		if t := s.trainingOf(ctx, in); t != nil {
			if mod := t.Module(in.Module); mod != nil {
				m.Title = mod.Title
			}
		}
		out = append(out, m)
	}
	return out, nil
}
