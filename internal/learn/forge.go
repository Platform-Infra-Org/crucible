package learn

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"crucible/internal/config"
	"crucible/internal/notify"
	"crucible/internal/rbac"
)

// Forge ranks and badges (spec §7). They are personal: nothing here compares one trainee with another.

type Notifier interface {
	Notify(ctx context.Context, ev notify.Event) error
}

type RankStep struct {
	Name string  `json:"name"`
	At   float64 `json:"at"` // % of enrolled training needed
}

var rankNames = []string{"Ore", "Ingot", "Tempered", "Blade", "Sword", "Masterwork"}

func Ladder(r config.RankThresholds) []RankStep {
	out := make([]RankStep, len(rankNames))
	for i, at := range r.Steps() {
		out[i] = RankStep{Name: rankNames[i], At: at}
	}
	return out
}

// RankFor is the highest level whose threshold pct reaches.
func RankFor(pct float64, ladder []RankStep) int {
	lvl := 0
	for i, s := range ladder {
		if pct >= s.At-1e-9 {
			lvl = i
		}
	}
	return lvl
}

// forgePercent is done/total as a percent, floored to one decimal. The epsilon keeps values that sit exactly on a
// threshold (0.22 of 1.1 is 19.999...) from flooring to the rank below.
func forgePercent(done, total float64) float64 {
	return math.Floor(done*1000/total+1e-6) / 10
}

type Badge struct {
	Training string    `json:"training"`
	Title    string    `json:"title"`
	EarnedAt time.Time `json:"earned_at"`
}

type ForgeView struct {
	Percent float64    `json:"percent"` // of all enrolled training, weighted by item points, one decimal
	Level   int        `json:"level"`   // highest ever earned
	Rank    string     `json:"rank"`
	Ladder  []RankStep `json:"ladder"`
	Badges  []Badge    `json:"badges"`
	RankUp  bool       `json:"rank_up"` // a new rank the trainee has not been shown yet
}

// UpdateForge recomputes the user's % and badges and raises the stored rank when a higher one is reached. The rank is
// never lowered. Crossing into a higher rank notifies the trainee and their mentors exactly once: the conditional
// upsert lets only one concurrent caller raise a level.
func (s *Service) UpdateForge(ctx context.Context, userID int64) (*ForgeView, error) {
	st, err := s.state()
	if err != nil {
		return nil, err
	}
	var email, name string
	if err := s.DB.QueryRow(ctx, `SELECT email, name FROM users WHERE id = $1`, userID).Scan(&email, &name); err != nil {
		return nil, err
	}
	var done, total float64
	titles := map[string]string{}
	for _, e := range (rbac.Checker{P: st.Platform}).Enrollments(email) {
		sd, err := s.Standing(ctx, userID, e.Team.ID, e.Program.Training)
		if err != nil {
			return nil, err
		}
		if sd == nil || sd.Total == 0 {
			continue // ponytail: unavailable content neither helps nor hurts the rank until it syncs again
		}
		done, total = done+sd.Done, total+sd.Total
		titles[sd.Training.ID] = sd.Training.Title
		if sd.Done >= sd.Total-1e-9 {
			if _, err := s.DB.Exec(ctx, `INSERT INTO badges (user_id, training, team) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
				userID, sd.Training.ID, e.Team.ID); err != nil {
				return nil, err
			}
		}
	}
	v := &ForgeView{Ladder: Ladder(st.Platform.Settings.Ranks), Badges: []Badge{}}
	if total > 0 {
		v.Percent = forgePercent(done, total)
	}
	lvl := RankFor(v.Percent, v.Ladder)
	// The first computation for a user only records where they already are (seen, no notification), so deploying this
	// does not announce a rank to everyone with existing progress.
	tag, err := s.DB.Exec(ctx, `INSERT INTO ranks (user_id, level, seen) VALUES ($1, $2, true) ON CONFLICT DO NOTHING`, userID, lvl)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		var raised int
		err = s.DB.QueryRow(ctx, `UPDATE ranks SET level = $2, earned_at = now(), seen = false WHERE user_id = $1 AND level < $2 RETURNING level`,
			userID, lvl).Scan(&raised)
		switch {
		case errors.Is(err, pgx.ErrNoRows): // not above the stored rank
		case err != nil:
			return nil, err
		default:
			s.rankUp(ctx, st.Platform, email, name, v.Ladder[raised].Name)
		}
	}
	if err := s.DB.QueryRow(ctx, `SELECT level, NOT seen FROM ranks WHERE user_id = $1`, userID).Scan(&v.Level, &v.RankUp); err != nil {
		return nil, err
	}
	v.Rank = v.Ladder[min(v.Level, len(v.Ladder)-1)].Name
	rows, err := s.DB.Query(ctx, `SELECT training, earned_at FROM badges WHERE user_id = $1 ORDER BY earned_at`, userID)
	if err != nil {
		return nil, err
	}
	badges, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Badge, error) {
		var b Badge
		err := r.Scan(&b.Training, &b.EarnedAt)
		if b.Title = titles[b.Training]; b.Title == "" {
			b.Title = b.Training // ponytail: content not loaded right now
		}
		return b, err
	})
	if err != nil {
		return nil, err
	}
	v.Badges = append(v.Badges, badges...)
	return v, nil
}

func (s *Service) rankUp(ctx context.Context, p *config.Platform, email, name, rank string) {
	if s.Notify == nil {
		return
	}
	to := []string{email}
	for _, t := range p.Teams {
		if m := t.Mentors[email]; m != "" && !slices.Contains(to, m) {
			to = append(to, m)
		}
	}
	who := name
	if who == "" {
		who = email
	}
	ev := notify.Event{Kind: notify.RankUp, To: to, Subject: who + " reached " + rank,
		Text: who + " is now " + rank + " in the Crucible. The forge remembers.", Link: "/"}
	if err := s.Notify.Notify(context.WithoutCancel(ctx), ev); err != nil {
		slog.Error("queueing the rank-up notification failed", "err", err)
	}
}

// SeenRankUp clears the banner only for the level the client was shown; a rank raised since stays unseen.
func (s *Service) SeenRankUp(ctx context.Context, userID int64, level int) error {
	_, err := s.DB.Exec(ctx, `UPDATE ranks SET seen = true WHERE user_id = $1 AND level = $2`, userID, level)
	return err
}
