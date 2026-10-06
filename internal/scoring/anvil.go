package scoring

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"crucible/internal/apperr"
	"crucible/internal/audit"
	"crucible/internal/auth"
	"crucible/internal/notify"
	"crucible/internal/rbac"
)

type Filter struct{ Training, Trainee, Type string }

// Queue lists pending submissions the user may score, oldest first (spec §7 "Scoring Queue … filter by
// training/trainee/type"). Nobody sees their own.
func (s *Service) Queue(ctx context.Context, u *auth.User, f Filter) ([]*Submission, error) {
	c, _, err := s.checker()
	if err != nil {
		return nil, err
	}
	list, err := collectSubs(s.DB.Query(ctx, `SELECT `+subCols+subFrom+`WHERE s.status = 'pending'
		AND ($1 = '' OR s.training = $1) AND ($2 = '' OR u.email = lower($2)) AND ($3 = '' OR s.qtype = $3)
		ORDER BY s.created_at, s.id LIMIT 500`, f.Training, strings.TrimSpace(f.Trainee), f.Type))
	if err != nil {
		return nil, err
	}
	// ponytail: RBAC filtered in Go over at most 500 rows (< 100 users); push into SQL if queues ever get long.
	list = slices.DeleteFunc(list, func(x *Submission) bool { return !c.Can(u.Email, rbac.Score, x.Team, x.Training, x.Email) })
	if list == nil {
		list = []*Submission{}
	}
	return list, nil
}

type Detail struct {
	Submission *Submission   `json:"submission"`
	History    []*Submission `json:"history"` // earlier answers to the same item, newest first
	Lab        *LabEvidence  `json:"lab,omitempty"`
}

// scorerGet loads a submission for someone who may score it. Its own trainee is told no (Forbidden); anyone else
// cannot tell it exists (NotFound), so submission ids can't be probed.
func (s *Service) scorerGet(ctx context.Context, u *auth.User, id int64) (*Submission, error) {
	sub, err := s.get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.mayScore(u, sub); err != nil {
		if errors.Is(err, apperr.Forbidden) && !strings.EqualFold(u.Email, sub.Email) {
			return nil, apperr.Wrap(apperr.NotFound, "submission not found")
		}
		return nil, err
	}
	return sub, nil
}

// MarshalJSON includes the rubric: Detail is only ever built for someone who may score the submission.
func (d *Detail) MarshalJSON() ([]byte, error) {
	hist := make([]any, len(d.History))
	for i, h := range d.History {
		hist[i] = h.ScorerView()
	}
	return json.Marshal(struct {
		Submission any          `json:"submission"`
		History    []any        `json:"history"`
		Lab        *LabEvidence `json:"lab,omitempty"`
	}{d.Submission.ScorerView(), hist, d.Lab})
}

func (s *Service) Detail(ctx context.Context, u *auth.User, id int64) (*Detail, error) {
	sub, err := s.scorerGet(ctx, u, id)
	if err != nil {
		return nil, err
	}
	hist, err := collectSubs(s.DB.Query(ctx, `SELECT `+subCols+subFrom+`WHERE s.user_id = $1 AND s.team = $2 AND s.training = $3
		AND s.module = $4 AND s.kind = $5 AND s.item = $6 AND s.id <> $7 ORDER BY s.id DESC`,
		sub.UserID, sub.Team, sub.Training, sub.Module, sub.Kind, sub.Item, sub.ID))
	if err != nil {
		return nil, err
	}
	if hist == nil {
		hist = []*Submission{}
	}
	d := &Detail{Submission: sub, History: hist}
	if sub.LabID != "" && s.Labs != nil {
		if d.Lab, err = s.Labs.Evidence(ctx, sub.LabID); err != nil { // e.g. its content version is gone: score without it
			s.log().Error("loading lab evidence failed", "submission", sub.ID, "lab", sub.LabID, "err", err)
			d.Lab = nil
		}
	}
	return d, nil
}

type SignOff struct {
	Team        string  `json:"team"`
	Training    string  `json:"training"`
	Module      string  `json:"module"`
	Question    string  `json:"question"`
	Prompt      string  `json:"prompt"`
	Points      float64 `json:"points"`
	Trainee     string  `json:"trainee"`
	TraineeName string  `json:"trainee_name"`
}

// SignOffs lists live demos the user can sign off: enrolled trainees (who have signed in) × signoff questions in the
// program's pinned version, without a pending or scored submission yet.
func (s *Service) SignOffs(ctx context.Context, u *auth.User) ([]SignOff, error) {
	c, st, err := s.checker()
	if err != nil {
		return nil, err
	}
	var cands []SignOff
	var emails []string
	for teamID, team := range st.Platform.Teams {
		for trID, p := range team.Programs {
			t, _ := st.ProgramTraining(teamID, trID)
			if t == nil {
				continue
			}
			for _, m := range t.Modules {
				if m.Quiz == nil {
					continue
				}
				for _, q := range m.Quiz.Questions {
					if q.Type != "signoff" {
						continue
					}
					for _, e := range p.Enrolled {
						if c.Can(u.Email, rbac.Score, teamID, trID, e) {
							cands = append(cands, SignOff{Team: teamID, Training: trID, Module: m.ID, Question: q.ID, Prompt: q.Prompt, Points: q.Points, Trainee: e})
							emails = append(emails, e)
						}
					}
				}
			}
		}
	}
	out := []SignOff{}
	if len(cands) == 0 {
		return out, nil
	}
	names := map[string]string{}
	rows, err := s.DB.Query(ctx, `SELECT email, name FROM users WHERE email = ANY($1)`, emails)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var e, n string
		if err := rows.Scan(&e, &n); err != nil {
			rows.Close()
			return nil, err
		}
		names[e] = n
	}
	rows.Close()
	type key struct{ email, team, training, module, item string }
	done := map[key]bool{}
	rows, err = s.DB.Query(ctx, `SELECT u.email, s.team, s.training, s.module, s.item FROM submissions s JOIN users u ON u.id = s.user_id
		WHERE s.qtype = 'signoff' AND s.status IN ('pending', 'scored') AND u.email = ANY($1)`, emails)
	if err != nil {
		return nil, err
	}
	keys, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (key, error) {
		var k key
		err := r.Scan(&k.email, &k.team, &k.training, &k.module, &k.item)
		return k, err
	})
	if err != nil {
		return nil, err
	}
	for _, k := range keys {
		done[k] = true
	}
	for _, x := range cands {
		name, signedIn := names[x.Trainee]
		if !signedIn || done[key{x.Trainee, x.Team, x.Training, x.Module, x.Question}] {
			continue
		}
		x.TraineeName = name
		out = append(out, x)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		return strings.Join([]string{a.Team, a.Training, a.Trainee, a.Module, a.Question}, "\x00") <
			strings.Join([]string{b.Team, b.Training, b.Trainee, b.Module, b.Question}, "\x00")
	})
	return out, nil
}

// Override changes an auto-checked task's points in a lab submission's module, with a reason (spec §7, audited in the
// same transaction as the change).
func (s *Service) Override(ctx context.Context, u *auth.User, id int64, task string, points float64, reason string) (*LabEvidence, error) {
	reason = Clean(strings.TrimSpace(reason))
	if reason == "" {
		return nil, apperr.Wrap(apperr.Invalid, "an override needs a reason")
	}
	if utf8.RuneCountInString(reason) > 500 {
		return nil, apperr.Wrap(apperr.Invalid, "keep the reason under 500 characters")
	}
	sub, err := s.scorerGet(ctx, u, id)
	if err != nil {
		return nil, err
	}
	if sub.LabID == "" || s.Labs == nil {
		return nil, apperr.Wrap(apperr.Conflict, "only lab submissions have check results to override")
	}
	err = s.Labs.Override(ctx, sub.LabID, task, points, func(ctx context.Context, tx pgx.Tx, prev float64) error {
		return audit.Log(ctx, tx, u.Email, "score.override", "lab/"+sub.LabID+"/"+task, map[string]any{"trainee": sub.Email,
			"submission": sub.ID, "from": prev, "to": points, "reason": reason}, "")
	})
	if err != nil {
		return nil, err
	}
	ev, err := s.Labs.Evidence(ctx, sub.LabID)
	if err != nil { // the override is stored and audited: a retry would only apply it twice
		s.log().Error("loading lab evidence after an override failed", "submission", sub.ID, "err", err)
		return nil, nil // the caller reloads the detail
	}
	return ev, nil
}

// File opens one uploaded file for someone who may view the trainee's progress; everyone else gets NotFound.
func (s *Service) File(ctx context.Context, u *auth.User, id int64, n int) (io.ReadCloser, string, error) {
	sub, err := s.get(ctx, id)
	if err != nil {
		return nil, "", err
	}
	if !s.CanView(u, sub.Team, sub.Training, sub.Email) || n < 0 || n >= len(sub.Keys) || n >= len(sub.Files) {
		return nil, "", apperr.Wrap(apperr.NotFound, "file not found")
	}
	rc, err := s.Blobs.Get(ctx, sub.Keys[n])
	if err != nil {
		return nil, "", fmt.Errorf("opening %s: %w", sub.Files[n].Name, err)
	}
	return rc, sub.Files[n].Name, nil
}

// ResetInput says what an admin clears for one trainee's quiz item: instant attempts, a final human score, or both.
type ResetInput struct {
	Reason   string `json:"reason"`
	Attempts bool   `json:"attempts"`
	Score    bool   `json:"score"`
}

// resetter is implemented by *learn.Service: it recomputes the item and may lower it (SetItem only ever raises).
type resetter interface {
	Reset(ctx context.Context, sub *Submission) error
}

// Reset is the admin escape hatch for "scores are final" (spec §7): it clears the trainee's instant quiz attempts for
// the submission's module and/or reopens the scored submission for scoring, audited in the same transaction, and
// tells the trainee. Admin only (spec §5.3: nobody else holds a power over another person's final score). Forge
// ranks are never lowered: UpdateForge only raises them.
func (s *Service) Reset(ctx context.Context, u *auth.User, id int64, in ResetInput) (*Submission, error) {
	c, _, err := s.checker()
	if err != nil {
		return nil, err
	}
	if !c.IsAdmin(u.Email) {
		return nil, apperr.Wrap(apperr.Forbidden, "only an admin can reset a trainee's item")
	}
	reason := Clean(strings.TrimSpace(in.Reason))
	switch {
	case reason == "":
		return nil, apperr.Wrap(apperr.Invalid, "a reset needs a reason")
	case utf8.RuneCountInString(reason) > 500:
		return nil, apperr.Wrap(apperr.Invalid, "keep the reason under 500 characters")
	case !in.Attempts && !in.Score:
		return nil, apperr.Wrap(apperr.Invalid, "choose what to reset")
	}
	sub, err := s.get(ctx, id)
	if err != nil {
		return nil, err
	}
	if sub.Kind != KindQuestion {
		return nil, apperr.Wrap(apperr.Conflict, "only quiz items can be reset")
	}
	if in.Score && sub.Status != Scored {
		return nil, apperr.Wrap(apperr.Conflict, "only a scored submission can be reopened")
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a no-op after Commit
	var cleared int64
	if in.Attempts {
		tag, err := tx.Exec(ctx, `DELETE FROM quiz_attempts WHERE user_id = $1 AND team = $2 AND training = $3 AND module = $4`,
			sub.UserID, sub.Team, sub.Training, sub.Module)
		if err != nil {
			return nil, err
		}
		cleared = tag.RowsAffected()
	}
	if in.Score {
		tag, err := tx.Exec(ctx, `UPDATE submissions SET status = 'pending', points = 0, scored_by = '', scored_at = NULL WHERE id = $1 AND status = 'scored'`, id)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, apperr.Wrap(apperr.Conflict, "this submission changed, reload it")
		}
	}
	if err := audit.Log(ctx, tx, u.Email, "submission.reset", fmt.Sprintf("submission/%d", id), map[string]any{"trainee": sub.Email,
		"item": sub.Team + "/" + sub.Training + "/" + sub.Module + "/" + sub.Item, "attempts_cleared": cleared, "score_reopened": in.Score,
		"previous_points": sub.Points, "reason": reason}, ""); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	if in.Score {
		sub.Status, sub.Points, sub.ScoredBy, sub.ScoredAt = Pending, 0, "", nil
	}
	if r, ok := s.Quiz.(resetter); ok {
		if err := r.Reset(ctx, sub); err != nil { // stored and audited: the next scoring decision recomputes anyway
			s.log().Error("recomputing progress after a reset failed", "submission", sub.ID, "err", err)
		}
	}
	s.send(ctx, notify.Event{Kind: notify.SubmissionScored, To: []string{sub.Email}, Subject: "Reset by an admin: " + sub.Training,
		Text: fmt.Sprintf("An admin reset your quiz item %q (%s / %s). Reason: %s", short(sub.Prompt), sub.Training, sub.Module, reason),
		Link: fmt.Sprintf("/p/%s/%s/m/%s/quiz", sub.Team, sub.Training, sub.Module)})
	return sub, nil
}
