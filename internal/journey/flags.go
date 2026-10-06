package journey

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"crucible/internal/content"
	"crucible/internal/gitsync"
)

// key is one person in one program.
type key struct {
	uid      int64
	training string
}

func (k key) mod(m string) string { return fmt.Sprint(k.uid, "/", k.training, "/", m) }

type signals struct {
	active map[string]bool    // key.mod → a lab is live in that module
	flags  map[key][]Flag     // failed_checks, final_hint, returned_twice (inactive is added per row)
	last   map[key]*time.Time // last activity by the trainee (scorer-side writes do not count)
	wait   map[key]bool       // a submission is waiting for a scorer
}

func notStarted() Flag { return Flag{Kind: "not_started", Detail: "has not started"} }

func inactive(n int, at *time.Time) Flag {
	return Flag{Kind: "inactive", Detail: fmt.Sprintf("no activity for %d business days", n), At: at}
}

// signals computes spec §11's stuck signals for everyone in uids across the team's programs, one query per signal.
func (s *Service) signals(ctx context.Context, st *gitsync.State, team string, uids []int64) (*signals, error) {
	sg := &signals{active: map[string]bool{}, flags: map[key][]Flag{}, last: map[key]*time.Time{}, wait: map[key]bool{}}
	var k key
	var m, item string
	var n int
	var at time.Time
	scan := []any{&k.uid, &k.training, &m, &item, &n, &at}

	rows, err := s.DB.Query(ctx, `SELECT DISTINCT user_id, training, module FROM lab_instances WHERE user_id = ANY($1) AND team = $2
		AND state IN ('pending_approval', 'provisioning', 'ready', 'destroying')`, uids, team)
	if err != nil {
		return nil, err
	}
	if _, err := pgx.ForEachRow(rows, []any{&k.uid, &k.training, &m}, func() error { sg.active[k.mod(m)] = true; return nil }); err != nil {
		return nil, err
	}
	// ≥ 3 failed checks on a task that is still neither passed nor skipped.
	rows, err = s.DB.Query(ctx, `SELECT li.user_id, li.training, li.module, cr.task, count(*), max(cr.at) FROM check_runs cr
		JOIN lab_instances li ON li.id = cr.lab_id
		WHERE li.user_id = ANY($1) AND li.team = $2 AND cr.exit_code <> 0
		  AND NOT EXISTS (SELECT 1 FROM lab_task_progress p WHERE p.user_id = li.user_id AND p.team = li.team
		                  AND p.training = li.training AND p.module = li.module AND p.task = cr.task)
		GROUP BY 1, 2, 3, 4 HAVING count(*) >= 3 ORDER BY 1, 2, 3, 4`, uids, team)
	if err != nil {
		return nil, err
	}
	if _, err := pgx.ForEachRow(rows, scan, func() error {
		at := at
		sg.flags[k] = append(sg.flags[k], Flag{Kind: "failed_checks", Module: m, Item: item, Detail: fmt.Sprintf("%d failed checks on %s", n, item), At: &at})
		return nil
	}); err != nil {
		return nil, err
	}
	// The final (solution) hint revealed (spec §8.4), judged against the program's current content.
	// It clears once the task is passed or skipped.
	rows, err = s.DB.Query(ctx, `SELECT user_id, training, module, task, max(hint_index), max(at) FROM hint_reveals h
		WHERE user_id = ANY($1) AND team = $2
		  AND NOT EXISTS (SELECT 1 FROM lab_task_progress p WHERE p.user_id = h.user_id AND p.team = h.team
		                  AND p.training = h.training AND p.module = h.module AND p.task = h.task)
		GROUP BY 1, 2, 3, 4 ORDER BY 1, 2, 3, 4`, uids, team)
	if err != nil {
		return nil, err
	}
	if _, err := pgx.ForEachRow(rows, scan, func() error {
		t, _ := st.ProgramTraining(team, k.training)
		if t == nil {
			return nil
		}
		if mod := t.Module(m); mod != nil && mod.Lab != nil {
			if tk := mod.Lab.Task(item); tk != nil && len(tk.Hints) > 0 && n >= len(tk.Hints)-1 {
				at := at
				sg.flags[k] = append(sg.flags[k], Flag{Kind: "final_hint", Module: m, Item: item, Detail: "revealed the final hint for " + item, At: &at})
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	// A submission returned for rework twice, until the item is scored.
	rows, err = s.DB.Query(ctx, `SELECT user_id, training, module, item, count(*), max(created_at) FROM submissions s
		WHERE user_id = ANY($1) AND team = $2 AND status = 'returned'
		  AND NOT EXISTS (SELECT 1 FROM submissions d WHERE d.user_id = s.user_id AND d.team = s.team AND d.training = s.training
		                  AND d.module = s.module AND d.kind = s.kind AND d.item = s.item AND d.status = 'scored')
		GROUP BY 1, 2, 3, 4 HAVING count(*) >= 2 ORDER BY 1, 2, 3, 4`, uids, team)
	if err != nil {
		return nil, err
	}
	if _, err := pgx.ForEachRow(rows, scan, func() error {
		at := at
		sg.flags[k] = append(sg.flags[k], Flag{Kind: "returned_twice", Module: m, Item: item, Detail: fmt.Sprintf("returned for rework %d times", n), At: &at})
		return nil
	}); err != nil {
		return nil, err
	}
	// Last activity by the trainee, for the inactive flag. item_progress counts only for readings (below): scorer
	// decisions bump quiz and lab rows, but only the trainee ever marks a reading done.
	rows, err = s.DB.Query(ctx, `SELECT user_id, training, max(at) FROM (
		SELECT user_id, training, created_at AS at FROM quiz_attempts WHERE user_id = ANY($1) AND team = $2
		UNION ALL SELECT user_id, training, last_activity_at FROM lab_instances WHERE user_id = ANY($1) AND team = $2
		UNION ALL SELECT li.user_id, li.training, cr.at FROM check_runs cr JOIN lab_instances li ON li.id = cr.lab_id WHERE li.user_id = ANY($1) AND li.team = $2
		UNION ALL SELECT user_id, training, updated_at FROM lab_task_progress WHERE user_id = ANY($1) AND team = $2
		UNION ALL SELECT user_id, training, at FROM hint_reveals WHERE user_id = ANY($1) AND team = $2
		UNION ALL SELECT user_id, training, created_at FROM submissions WHERE user_id = ANY($1) AND team = $2) a
		GROUP BY 1, 2`, uids, team)
	if err != nil {
		return nil, err
	}
	if _, err = pgx.ForEachRow(rows, []any{&k.uid, &k.training, &at}, func() error { at := at; sg.last[k] = &at; return nil }); err != nil {
		return nil, err
	}
	rows, err = s.DB.Query(ctx, `SELECT user_id, training, module, item, updated_at FROM item_progress
		WHERE user_id = ANY($1) AND team = $2 AND status = 'complete'`, uids, team)
	if err != nil {
		return nil, err
	}
	var rm, ri string
	if _, err = pgx.ForEachRow(rows, []any{&k.uid, &k.training, &rm, &ri, &at}, func() error {
		t, _ := st.ProgramTraining(team, k.training)
		if t == nil || !isReading(t, rm, ri) {
			return nil
		}
		if prev := sg.last[k]; prev == nil || at.After(*prev) {
			at := at
			sg.last[k] = &at
		}
		return nil
	}); err != nil {
		return nil, err
	}
	// Submissions waiting for a scorer: the stuck party is the scorer, not the trainee.
	rows, err = s.DB.Query(ctx, `SELECT DISTINCT user_id, training FROM submissions WHERE user_id = ANY($1) AND team = $2 AND status = 'pending'`, uids, team)
	if err != nil {
		return nil, err
	}
	_, err = pgx.ForEachRow(rows, []any{&k.uid, &k.training}, func() error { sg.wait[k] = true; return nil })
	return sg, err
}

// BusinessDays counts the whole Mon–Fri days between from and to, in UTC: from's date and to's (partial) date are not counted.
// ponytail: no holidays and no team time zones; add a holiday list to platform.yaml if teams ask for it.
func BusinessDays(from, to time.Time) int {
	from, to = from.UTC().Truncate(24*time.Hour), to.UTC().Truncate(24*time.Hour)
	n := 0
	for d := from.AddDate(0, 0, 1); d.Before(to); d = d.AddDate(0, 0, 1) {
		if wd := d.Weekday(); wd != time.Saturday && wd != time.Sunday {
			n++
		}
	}
	return n
}

func isReading(t *content.Training, module, item string) bool {
	m := t.Module(module)
	return m != nil && slices.ContainsFunc(m.Items, func(it content.Item) bool { return it.Kind == "reading" && it.ID == item })
}
