package journey

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

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
	last   map[key]*time.Time // last activity
}

func inactive(n int, at *time.Time) Flag {
	return Flag{Kind: "inactive", Detail: fmt.Sprintf("no activity for %d business days", n), At: at}
}

// signals computes spec §11's stuck signals for everyone in uids across the team's programs, one query per signal.
func (s *Service) signals(ctx context.Context, st *gitsync.State, team string, uids []int64) (*signals, error) {
	sg := &signals{active: map[string]bool{}, flags: map[key][]Flag{}, last: map[key]*time.Time{}}
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
	rows, err = s.DB.Query(ctx, `SELECT user_id, training, module, task, max(hint_index), max(at) FROM hint_reveals
		WHERE user_id = ANY($1) AND team = $2 GROUP BY 1, 2, 3, 4 ORDER BY 1, 2, 3, 4`, uids, team)
	if err != nil {
		return nil, err
	}
	if _, err := pgx.ForEachRow(rows, scan, func() error {
		t, _ := st.ProgramTraining(team, k.training)
		if t == nil {
			return nil
		}
		if mod := t.Module(m); mod != nil && mod.Lab != nil {
			if tk := mod.Lab.Task(item); tk != nil && len(tk.Hints) > 0 && n == len(tk.Hints)-1 {
				at := at
				sg.flags[k] = append(sg.flags[k], Flag{Kind: "final_hint", Module: m, Item: item, Detail: "revealed the final hint for " + item, At: &at})
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	// A submission returned for rework twice.
	rows, err = s.DB.Query(ctx, `SELECT user_id, training, module, item, count(*), max(created_at) FROM submissions
		WHERE user_id = ANY($1) AND team = $2 AND status = 'returned'
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
	// Last activity, for the 5-business-day inactive flag.
	rows, err = s.DB.Query(ctx, `SELECT user_id, training, max(at) FROM (
		SELECT user_id, training, updated_at AS at FROM item_progress WHERE user_id = ANY($1) AND team = $2
		UNION ALL SELECT user_id, training, created_at FROM quiz_attempts WHERE user_id = ANY($1) AND team = $2
		UNION ALL SELECT user_id, training, last_activity_at FROM lab_instances WHERE user_id = ANY($1) AND team = $2
		UNION ALL SELECT user_id, training, at FROM hint_reveals WHERE user_id = ANY($1) AND team = $2
		UNION ALL SELECT user_id, training, created_at FROM submissions WHERE user_id = ANY($1) AND team = $2) a
		GROUP BY 1, 2`, uids, team)
	if err != nil {
		return nil, err
	}
	_, err = pgx.ForEachRow(rows, []any{&k.uid, &k.training, &at}, func() error { at := at; sg.last[k] = &at; return nil })
	return sg, err
}

// BusinessDays counts Mon–Fri dates after from's date, up to and including to's date, in UTC.
// ponytail: no holidays and no team time zones; add a holiday list to platform.yaml if teams ask for it.
func BusinessDays(from, to time.Time) int {
	from, to = from.UTC().Truncate(24*time.Hour), to.UTC().Truncate(24*time.Hour)
	n := 0
	for d := from.AddDate(0, 0, 1); !d.After(to); d = d.AddDate(0, 0, 1) {
		if wd := d.Weekday(); wd != time.Saturday && wd != time.Sunday {
			n++
		}
	}
	return n
}
