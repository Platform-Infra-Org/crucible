// Package journey answers "how are my people doing?" for leaders, managers, scorers and mentors (spec §11): a module
// heat map per trainee and program, with stuck flags, and a mentor dashboard. It only reads. It lists people one by
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

type Pending struct {
	ID        int64     `json:"id"`
	Training  string    `json:"training"`
	Module    string    `json:"module"`
	Item      string    `json:"item"`
	Type      string    `json:"type"`
	CreatedAt time.Time `json:"created_at"`
}

type Failure struct {
	Training string    `json:"training"`
	Module   string    `json:"module"`
	Task     string    `json:"task,omitempty"`
	Kind     string    `json:"kind"` // lab_failed | check_failed
	Detail   string    `json:"detail"`
	At       time.Time `json:"at"`
}

type Mentee struct {
	Email    string    `json:"email"`
	Name     string    `json:"name"`
	Team     string    `json:"team"`
	Rank     string    `json:"rank"`
	Programs []Row     `json:"programs"`
	Pending  []Pending `json:"pending"`
	Failures []Failure `json:"failures"`
}

// want is one (program, person) pair a page shows.
type want struct{ training, email string }

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

// Team lists every enrolled person the actor may follow (spec §5.3 "View trainee progress"), program by program,
// excluding the actor themselves.
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
	for _, tr := range slices.Sorted(maps.Keys(t.Programs)) {
		for _, email := range slices.Sorted(slices.Values(t.Programs[tr].Enrolled)) {
			if email != me && c.Can(me, rbac.ViewProgress, team, tr, email) {
				ws = append(ws, want{tr, email})
			}
		}
	}
	people, err := s.people(ctx, ws)
	if err != nil {
		return nil, err
	}
	return s.rows(ctx, st, team, ws, people)
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

func (s *Service) rows(ctx context.Context, st *gitsync.State, team string, ws []want, people map[string]person) ([]Row, error) {
	out := []Row{}
	if len(ws) == 0 {
		return out, nil
	}
	uids := []int64{}
	for _, p := range people {
		uids = append(uids, p.id)
	}
	sig, err := s.signals(ctx, st, team, uids)
	if err != nil {
		return nil, err
	}
	for _, w := range ws {
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

// Mentees is the mentor dashboard (spec §11): each mentee's programs, rank, submissions waiting for a scorer, and
// recent lab failures. Only people whose mentor (team.yaml) is the caller, and only within that team.
func (s *Service) Mentees(ctx context.Context, u *auth.User) ([]Mentee, error) {
	st, err := s.state()
	if err != nil {
		return nil, err
	}
	me, out := strings.ToLower(u.Email), []Mentee{}
	ladder := learn.Ladder(st.Platform.Settings.Ranks)
	for _, teamID := range slices.Sorted(maps.Keys(st.Platform.Teams)) {
		t := st.Platform.Teams[teamID]
		var mentees []string
		var ws []want
		for _, trainee := range slices.Sorted(maps.Keys(t.Mentors)) {
			if t.Mentors[trainee] != me || trainee == me {
				continue
			}
			mentees = append(mentees, trainee)
			for _, tr := range slices.Sorted(maps.Keys(t.Programs)) {
				if slices.Contains(t.Programs[tr].Enrolled, trainee) {
					ws = append(ws, want{tr, trainee})
				}
			}
		}
		if len(mentees) == 0 {
			continue
		}
		all := ws
		for _, e := range mentees {
			all = append(all, want{"", e}) // mentees enrolled in nothing still get a name and rank
		}
		people, err := s.people(ctx, all)
		if err != nil {
			return nil, err
		}
		rows, err := s.rows(ctx, st, teamID, ws, people)
		if err != nil {
			return nil, err
		}
		d, err := s.details(ctx, teamID, people)
		if err != nil {
			return nil, err
		}
		for _, e := range mentees {
			m := Mentee{Email: e, Team: teamID, Rank: ladder[0].Name, Programs: []Row{}, Pending: []Pending{}, Failures: []Failure{}}
			for _, r := range rows {
				if r.Email == e {
					m.Programs = append(m.Programs, r)
				}
			}
			if p, ok := people[e]; ok {
				m.Name = p.name
				if lvl, ok := d.level[p.id]; ok {
					m.Rank = ladder[min(lvl, len(ladder)-1)].Name // the persisted, never-lowered rank
				}
				m.Pending = append(m.Pending, d.pending[p.id]...)
				m.Failures = append(m.Failures, d.failures[p.id]...)
			}
			out = append(out, m)
		}
	}
	return out, nil
}

type details struct {
	level    map[int64]int
	pending  map[int64][]Pending
	failures map[int64][]Failure
}

// details loads ranks, pending submissions and recent failures for a team's mentees, one query each. Only ids, item
// names and types leave here: never prompts, rubrics, answers or check output.
func (s *Service) details(ctx context.Context, team string, people map[string]person) (*details, error) {
	d := &details{level: map[int64]int{}, pending: map[int64][]Pending{}, failures: map[int64][]Failure{}}
	uids := []int64{}
	for _, p := range people {
		uids = append(uids, p.id)
	}
	var uid int64
	var lvl int
	rows, err := s.DB.Query(ctx, `SELECT user_id, level FROM ranks WHERE user_id = ANY($1)`, uids)
	if err != nil {
		return nil, err
	}
	if _, err := pgx.ForEachRow(rows, []any{&uid, &lvl}, func() error { d.level[uid] = lvl; return nil }); err != nil {
		return nil, err
	}
	var p Pending
	rows, err = s.DB.Query(ctx, `SELECT user_id, id, training, module, item, qtype, created_at FROM submissions
		WHERE user_id = ANY($1) AND team = $2 AND status = 'pending' ORDER BY created_at, id`, uids, team)
	if err != nil {
		return nil, err
	}
	if _, err := pgx.ForEachRow(rows, []any{&uid, &p.ID, &p.Training, &p.Module, &p.Item, &p.Type, &p.CreatedAt}, func() error {
		d.pending[uid] = append(d.pending[uid], p)
		return nil
	}); err != nil {
		return nil, err
	}
	var f Failure
	rows, err = s.DB.Query(ctx, `
		SELECT user_id, training, module, task, kind, detail, at FROM (
		  SELECT *, row_number() OVER (PARTITION BY user_id ORDER BY at DESC) AS n FROM (
		    SELECT user_id, training, module, '' AS task, 'lab_failed' AS kind, left(error, 200) AS detail, created_at AS at
		      FROM lab_instances WHERE user_id = ANY($1) AND team = $2 AND state = 'failed'
		       AND created_at > $3::timestamptz - interval '14 days'
		    UNION ALL
		    SELECT li.user_id, li.training, li.module, cr.task, 'check_failed', count(*)::text || ' failed checks', max(cr.at)
		      FROM check_runs cr JOIN lab_instances li ON li.id = cr.lab_id
		     WHERE li.user_id = ANY($1) AND li.team = $2 AND cr.exit_code <> 0 AND cr.at > $3::timestamptz - interval '7 days'
		     GROUP BY li.user_id, li.training, li.module, cr.task) f) x
		WHERE n <= 20 ORDER BY user_id, at DESC`, uids, team, s.Now())
	if err != nil {
		return nil, err
	}
	_, err = pgx.ForEachRow(rows, []any{&uid, &f.Training, &f.Module, &f.Task, &f.Kind, &f.Detail, &f.At}, func() error {
		d.failures[uid] = append(d.failures[uid], f)
		return nil
	})
	return d, err
}
