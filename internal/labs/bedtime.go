package labs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"crucible/internal/apperr"
	"crucible/internal/audit"
	"crucible/internal/auth"
	"crucible/internal/content"
)

// TeardownActor is who `crucible aws teardown` turns the kill switch on as; Morning turns off only its switch.
const TeardownActor = "crucible aws teardown"

const bankingMsg = "The forge is banking for the night: labs start again in the morning."

// settleFor is how long labs starting at bedtime get to finish before they are cancelled.
const settleFor = 10 * time.Minute

// Bedtime is the platform going to sleep (spec 2026-11-05 §2): one row while it lasts.
type Bedtime struct {
	RequestedAt time.Time
	By, Phase   string // settling | sleeping | asleep
}

// RequestBedtime starts the platform's bedtime (a no-op within the same night; a row left from an earlier night starts over); the server's sweep carries it out.
func RequestBedtime(ctx context.Context, db *pgxpool.Pool, by string, now time.Time) error {
	_, err := db.Exec(ctx, `INSERT INTO platform_bedtime (requested_at, requested_by) VALUES ($1, $2)
		ON CONFLICT (id) DO UPDATE SET requested_at = EXCLUDED.requested_at, requested_by = EXCLUDED.requested_by, phase = 'settling'
		WHERE platform_bedtime.requested_at < EXCLUDED.requested_at - interval '12 hours'`, now, by)
	return err
}

// CancelBedtime ends it: labs may start again (sleeping labs stay asleep until their trainees wake them).
func CancelBedtime(ctx context.Context, db *pgxpool.Pool) error {
	_, err := db.Exec(ctx, `DELETE FROM platform_bedtime`)
	return err
}

func ReadBedtime(ctx context.Context, db *pgxpool.Pool) (*Bedtime, error) {
	var b Bedtime
	err := db.QueryRow(ctx, `SELECT requested_at, requested_by, phase FROM platform_bedtime`).Scan(&b.RequestedAt, &b.By, &b.Phase)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &b, err
}

func labList(ctx context.Context, db *pgxpool.Pool, sql, format string) ([]string, error) {
	rows, err := db.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	out := []string{}
	var id, runtime, state string
	_, err = pgx.ForEachRow(rows, []any{&id, &runtime, &state}, func() error {
		out = append(out, fmt.Sprintf(format, id, runtime, state))
		return nil
	})
	return out, err
}

// AwakeAWSLabs is every aws lab that would still run its machines overnight: starting, ready, waking or being destroyed.
func AwakeAWSLabs(ctx context.Context, db *pgxpool.Pool) ([]string, error) {
	return labList(ctx, db, `SELECT id, runtime, state FROM lab_instances WHERE runtime = 'aws'
		AND state IN ('provisioning', 'ready', 'waking', 'destroying') ORDER BY id`, "%[1]s (%[3]s)")
}

// ActiveLabs is every lab not yet ended: what a teardown waits for.
func ActiveLabs(ctx context.Context, db *pgxpool.Pool) ([]string, error) {
	return labList(ctx, db, `SELECT id, runtime, state FROM lab_instances
		WHERE state IN ('provisioning', 'ready', 'asleep', 'waking', 'destroying') ORDER BY id`, "%s (%s, %s)")
}

// StopAll turns the kill switch on for a teardown, with its audit entry; the sweep then ends every lab.
func StopAll(ctx context.Context, db *pgxpool.Pool, by string, now time.Time) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	tag, err := tx.Exec(ctx, `UPDATE kill_switch SET enabled = true, changed_by = $1, changed_at = $2 WHERE NOT enabled`, by, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 1 { // an admin's switch already on stays theirs
		if err := audit.Log(ctx, tx, by, "kill_switch.on", "", map[string]any{"why": "teardown"}, ""); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// Morning runs once when the server starts (the platform is awake): its bedtime ends, and a kill switch a teardown
// turned on is turned off. Sleeping labs stay asleep until their trainees wake them; an admin's kill switch stays.
func (s *Service) Morning(ctx context.Context) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `DELETE FROM platform_bedtime`); err != nil {
		return err
	}
	var since *time.Time
	err = tx.QueryRow(ctx, `SELECT changed_at FROM kill_switch WHERE enabled AND changed_by = $1 FOR UPDATE`, TeardownActor).Scan(&since)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err == nil {
		now := s.Now()
		if since != nil { // escalation was paused: give pending requests their remaining time back, as SetKillSwitch does
			if _, err := tx.Exec(ctx, `UPDATE lab_instances SET escalate_at = escalate_at + ($1::timestamptz - $2::timestamptz)
				WHERE state = 'pending_approval'`, now, *since); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE kill_switch SET enabled = false, changed_by = 'crucible serve', changed_at = $1`, now); err != nil {
			return err
		}
		if err := audit.Log(ctx, tx, "crucible serve", "kill_switch.off", "", map[string]any{"after": TeardownActor}, ""); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// labSleeper is a runtime whose labs can sleep with the platform (aws).
type labSleeper interface {
	SleepLab(ctx context.Context, inst *Instance, lab *content.Lab) (string, error)
	WakeLab(ctx context.Context, inst *Instance, lab *content.Lab) error
}

// bedtimeStep (every sweep) carries out a requested bedtime: labs starting get settleFor, then one still starting is
// cancelled; every ready aws lab is put to sleep in the background; the phase is asleep once none is awake.
func (s *Service) bedtimeStep(ctx context.Context) {
	b, err := ReadBedtime(ctx, s.DB)
	if err != nil || b == nil {
		return
	}
	now := s.Now()
	if b.Phase == "settling" {
		var starting int
		if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM lab_instances WHERE runtime = 'aws' AND state IN ('provisioning', 'destroying')`).Scan(&starting); err != nil {
			s.Log.Error("bedtime: counting starting labs failed", "err", err)
			return
		}
		if starting > 0 && now.Before(b.RequestedAt.Add(settleFor)) {
			return
		}
		rows, err := s.DB.Query(ctx, `SELECT `+instCols+` FROM lab_instances WHERE runtime = 'aws' AND state = 'provisioning'`)
		if err != nil {
			s.Log.Error("bedtime: listing starting labs failed", "err", err)
			return
		}
		late, err := collectInst(rows)
		if err != nil {
			s.Log.Error("bedtime: listing starting labs failed", "err", err)
			return
		}
		for _, inst := range late {
			s.destroy(ctx, inst, "bedtime")
		}
		if _, err := s.DB.Exec(ctx, `UPDATE platform_bedtime SET phase = 'sleeping'`); err != nil {
			s.Log.Error("bedtime: moving on failed", "err", err)
			return
		}
	}
	rows, err := s.DB.Query(ctx, `SELECT `+instCols+` FROM lab_instances WHERE runtime = 'aws' AND state = 'ready'`)
	if err != nil {
		s.Log.Error("bedtime: listing ready labs failed", "err", err)
		return
	}
	ready, err := collectInst(rows)
	if err != nil {
		s.Log.Error("bedtime: listing ready labs failed", "err", err)
		return
	}
	for _, inst := range ready {
		var failedRecently bool // a failed sleep is retried every 5 minutes, not every sweep
		if err := s.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM lab_events WHERE lab_id = $1 AND kind = 'sleep_failed' AND at > $2)`,
			inst.ID, now.Add(-5*time.Minute)).Scan(&failedRecently); err != nil || failedRecently {
			continue
		}
		s.once("sleep:"+inst.ID, func() { s.sleepLab(context.WithoutCancel(ctx), inst) }) // its own slot: a destroy keeps the lab id's
	}
	if awake, err := AwakeAWSLabs(ctx, s.DB); err == nil && len(awake) == 0 {
		_, _ = s.DB.Exec(ctx, `UPDATE platform_bedtime SET phase = 'asleep'`)
	}
}

// sleepLab puts one ready aws lab to sleep. A failure leaves it ready (the next sweep tries again) and is logged.
func (s *Service) sleepLab(ctx context.Context, inst *Instance) {
	sl, ok := s.Runners[inst.Runtime].(labSleeper)
	if !ok {
		return
	}
	lab, _, err := s.labContent(ctx, inst)
	if err != nil {
		s.Log.Error("bedtime: the lab's content is gone", "lab", inst.ID, "err", err)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	how, err := sl.SleepLab(ctx, inst, lab)
	wctx, wcancel := finalCtx(ctx)
	defer wcancel()
	if err != nil {
		s.Log.Error("bedtime: a lab did not go to sleep", "lab", inst.ID, "err", err)
		_, _ = s.DB.Exec(wctx, `INSERT INTO lab_events (lab_id, kind, detail, at) VALUES ($1, 'sleep_failed', $2, $3)`,
			inst.ID, cleanText(err.Error()), s.Now())
		return
	}
	now := s.Now()
	tag, err := s.DB.Exec(wctx, `UPDATE lab_instances SET state = 'asleep', slept_at = $2, slept_as = $3 WHERE id = $1 AND state = 'ready'`,
		inst.ID, now, how)
	if err != nil || tag.RowsAffected() == 0 {
		return // ended meanwhile: its destroy removes the sleeping machines
	}
	s.event(wctx, inst.ID, "sleep", how)
	if err := s.meterSleep(wctx, inst.ID, now); err != nil {
		s.Log.Error("metering the lab's sleep failed; it stays priced as running", "lab", inst.ID, "err", err)
	}
}

// Wake (the lab's trainee): a sleeping lab's machines start and its workspace comes back, in the background.
func (s *Service) Wake(ctx context.Context, u *auth.User, labID string) (*View, error) {
	inst, err := s.owned(ctx, u, labID)
	if err != nil {
		return nil, err
	}
	if inst.State != Asleep {
		return nil, apperr.Wrap(apperr.Conflict, "this lab isn't asleep")
	}
	if b, err := ReadBedtime(ctx, s.DB); err != nil {
		return nil, err
	} else if b != nil {
		return nil, apperr.Wrap(apperr.Conflict, bankingMsg)
	}
	if ks, err := s.KillSwitch(ctx); err != nil {
		return nil, err
	} else if ks.Enabled {
		return nil, apperr.Wrap(apperr.Conflict, "Labs are paused by an admin.")
	}
	if inst.EndsAt != nil && !inst.EndsAt.After(s.Now()) {
		return nil, apperr.Wrap(apperr.Conflict, "Time ran out on this lab while it slept.")
	}
	sl, ok := s.Runners[inst.Runtime].(labSleeper)
	if !ok {
		return nil, apperr.Wrap(apperr.Unavailable, fmt.Sprintf("%s labs can't be woken on this server", inst.Runtime))
	}
	lab, _, err := s.labContent(ctx, inst)
	if err != nil {
		return nil, err
	}
	tag, err := s.DB.Exec(ctx, `UPDATE lab_instances SET state = 'waking', waking_since = $2, error = '' WHERE id = $1 AND state = 'asleep'`,
		inst.ID, s.Now())
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, apperr.Wrap(apperr.Conflict, "the lab is changing state; try again")
	}
	s.event(ctx, inst.ID, "waking", "")
	// its own slot: a destroy (End during the wake) takes the lab id's and waits for the runner's per-lab lock
	if !s.once("wake:"+inst.ID, func() { s.wakeLab(context.WithoutCancel(ctx), inst, lab, sl) }) {
		s.endRow(ctx, inst.ID, `UPDATE lab_instances SET state = 'asleep', waking_since = NULL WHERE id = $1 AND state = 'waking'`)
		return nil, apperr.Wrap(apperr.Conflict, "the lab is changing state; try again")
	}
	return s.Get(ctx, u, labID)
}

func (s *Service) wakeLab(ctx context.Context, inst *Instance, lab *content.Lab, sl labSleeper) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	err := sl.WakeLab(ctx, inst, lab)
	wctx, wcancel := finalCtx(ctx)
	defer wcancel()
	if err != nil {
		s.Log.Warn("waking a lab failed", "lab", inst.ID, "err", err)
		s.endRow(wctx, inst.ID, `UPDATE lab_instances SET state = 'asleep', waking_since = NULL, error = $2
			WHERE id = $1 AND state = 'waking'`, cleanText(err.Error()))
		s.event(wctx, inst.ID, "wake_failed", cleanText(err.Error()))
		return
	}
	now := s.Now()
	tag, err := s.DB.Exec(wctx, `UPDATE lab_instances SET state = 'ready', woke_at = $2, last_activity_at = $2, waking_since = NULL
		WHERE id = $1 AND state = 'waking'`, inst.ID, now)
	if err != nil || tag.RowsAffected() == 0 {
		return // ended while waking: that destroy removes everything
	}
	s.event(wctx, inst.ID, "woke", "")
	if err := s.EndMeter(wctx, inst.ID, now); err != nil { // the sleeping rows end; the next sweep meters it running
		s.Log.Error("ending the lab's sleeping meter failed", "lab", inst.ID, "err", err)
	}
}
