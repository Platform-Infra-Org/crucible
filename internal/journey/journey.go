// Package journey answers "how are my people doing?" for leaders, seniors and the people who run a training (spec §11):
// a module heat map per participant and training, with stuck flags. It only reads. It lists people one by
// one, sorted by email, and never ranks or compares them (spec §1).
package journey

import (
	"context"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"crucible/internal/apperr"
	"crucible/internal/auth"
	"crucible/internal/gitsync"
	"crucible/internal/learn"
	"crucible/internal/rbac"
)

type Service struct {
	DB    *pgxpool.Pool
	Learn *learn.Service
	Now   func() time.Time
}

type Cell struct {
	Module string `json:"module"`
	Title  string `json:"title"`
	Heat   string `json:"heat"` // cold | glowing | forged
}

type Flag struct {
	Kind   string     `json:"kind"` // failed_checks | final_hint | inactive | returned_twice
	Module string     `json:"module,omitempty"`
	Item   string     `json:"item,omitempty"`
	Detail string     `json:"detail"`
	At     *time.Time `json:"at,omitempty"`
}

type Row struct {
	Email      string     `json:"email"`
	Name       string     `json:"name"`
	Avatar     string     `json:"avatar"` // the person's forge icon, "" for initials
	Team       string     `json:"team"`
	Training   string     `json:"training"`
	Title      string     `json:"title"`
	Percent    int        `json:"percent"`
	Cells      []Cell     `json:"cells"`
	Flags      []Flag     `json:"flags"`
	LastActive *time.Time `json:"last_active,omitempty"`
}

// want is one (training, person) pair a page shows; team owns the training.
type want struct{ team, training, email string }

type person struct {
	id     int64
	name   string
	avatar string
}

func (s *Service) state() (*gitsync.State, error) {
	st := s.Learn.State()
	if st == nil || st.Platform == nil {
		return nil, apperr.Wrap(apperr.Unavailable, "content is still syncing, try again in a moment")
	}
	return st, nil
}

// Team lists, excluding the actor themselves, the participants of the trainings the team owns that the actor may
// follow, and the team's own people in trainings other teams own (its leaders and seniors follow them there).
func (s *Service) Team(ctx context.Context, u *auth.User, team string) ([]Row, error) {
	st, err := s.state()
	if err != nil {
		return nil, err
	}
	t := st.Platform.Teams[team]
	if t == nil {
		return nil, apperr.Wrap(apperr.NotFound, "team not found")
	}
	c, me := rbac.Checker{P: st.Platform}, strings.ToLower(u.Email)
	var ws []want
	for _, owner := range slices.Sorted(maps.Keys(st.Platform.Teams)) {
		ot := st.Platform.Teams[owner]
		for _, tr := range slices.Sorted(maps.Keys(ot.Programs)) {
			for _, email := range ot.Programs[tr].Enrolled {
				if email != me && (owner == team || t.RoleOf(email) != "") && c.Can(me, rbac.ViewProgress, owner, tr, email) {
					ws = append(ws, want{owner, tr, email})
				}
			}
		}
	}
	people, err := s.people(ctx, ws)
	if err != nil {
		return nil, err
	}
	return s.rows(ctx, st, ws, people)
}

// people looks up everyone in ws in one query; someone who never signed in is absent.
func (s *Service) people(ctx context.Context, ws []want) (map[string]person, error) {
	emails := []string{}
	for _, w := range ws {
		emails = append(emails, w.email)
	}
	rows, err := s.DB.Query(ctx, `SELECT DISTINCT ON (email) email, id, name, avatar FROM users
		WHERE email = ANY($1) ORDER BY email, id DESC`, emails)
	if err != nil {
		return nil, err
	}
	out := map[string]person{}
	var e string
	var p person
	_, err = pgx.ForEachRow(rows, []any{&e, &p.id, &p.name, &p.avatar}, func() error { out[e] = p; return nil })
	return out, err
}

// rows builds one row per want, in order. Signals are read once per owning team.
func (s *Service) rows(ctx context.Context, st *gitsync.State, ws []want, people map[string]person) ([]Row, error) {
	out := []Row{}
	if len(ws) == 0 {
		return out, nil
	}
	uids := []int64{}
	for _, p := range people {
		uids = append(uids, p.id)
	}
	sigs := map[string]*signals{}
	for _, w := range ws {
		team := w.team
		sig := sigs[team]
		if sig == nil {
			var err error
			if sig, err = s.signals(ctx, st, team, uids); err != nil {
				return nil, err
			}
			sigs[team] = sig
		}
		r := Row{Email: w.email, Team: team, Training: w.training, Title: w.training, Cells: []Cell{}, Flags: []Flag{}}
		t, _ := st.ProgramTraining(team, w.training)
		if t == nil {
			out = append(out, r) // content unavailable: an honest empty row
			continue
		}
		r.Title = t.Title
		p, ok := people[w.email]
		if !ok {
			for _, m := range t.Modules {
				r.Cells = append(r.Cells, Cell{Module: m.ID, Title: m.Title, Heat: "cold"})
			}
			r.Flags = append(r.Flags, Flag{Kind: "inactive", Detail: "has not signed in yet"})
			out = append(out, r)
			continue
		}
		r.Name, r.Avatar = p.name, p.avatar
		// ponytail: Standing is one small indexed query per (person, program); batch it inside learn if pages grow past a few hundred rows.
		sd, err := s.Learn.Standing(ctx, p.id, team, w.training)
		if err != nil {
			return nil, err
		}
		if sd == nil {
			out = append(out, r)
			continue
		}
		k := key{p.id, w.training}
		for _, mv := range sd.Modules {
			heat := "cold"
			if mv.Complete {
				heat = "forged"
			} else if sig.active[k.mod(mv.ID)] || slices.ContainsFunc(mv.Items, func(it learn.ItemView) bool { return it.Status != "new" }) {
				heat = "glowing"
			}
			r.Cells = append(r.Cells, Cell{Module: mv.ID, Title: mv.Title, Heat: heat})
		}
		r.Percent = sd.Percent
		r.Flags = append(r.Flags, sig.flags[k]...)
		r.LastActive = sig.last[k]
		// Enrolment time is not stored, so with no activity the honest statement is "not started", not a day count.
		if sd.Percent < 100 && !sig.wait[k] {
			if r.LastActive == nil {
				r.Flags = append(r.Flags, notStarted())
			} else if n := BusinessDays(*r.LastActive, s.Now()); n >= 5 {
				r.Flags = append(r.Flags, inactive(n, r.LastActive))
			}
		}
		out = append(out, r)
	}
	return out, nil
}
