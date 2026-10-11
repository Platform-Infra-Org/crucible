package learn

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"time"

	"github.com/jackc/pgx/v5"

	"crucible/internal/apperr"
	"crucible/internal/config"
	"crucible/internal/notify"
	"crucible/internal/rbac"
	"crucible/internal/smith"
)

// Sword ranks, the smith rank and badges (spec 2026-10-11). Nothing here orders one person against another.

type Notifier interface {
	Notify(ctx context.Context, ev notify.Event) error
}

type RankStep struct {
	Name string  `json:"name"`
	At   float64 `json:"at"` // % of this training needed
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

type Sword struct {
	Training string    `json:"training"`
	Title    string    `json:"title"`
	Level    int       `json:"level"` // highest ever reached
	Rank     string    `json:"rank"`
	Percent  float64   `json:"percent"` // of this training, weighted by item points, one decimal
	Next     *RankStep `json:"next"`
}

type SmithView struct {
	XP    int         `json:"xp"`
	Level int         `json:"level"`
	Title string      `json:"title"`
	Next  *smith.Step `json:"next"`
}

type SwordUp struct {
	Training string `json:"training"`
	Title    string `json:"title"`
	Level    int    `json:"level"`
	Rank     string `json:"rank"`
}

type RankUps struct {
	Smith  *smith.Ref `json:"smith"`
	Swords []SwordUp  `json:"swords"`
}

type ForgeView struct {
	Smith       SmithView    `json:"smith"`
	Swords      []Sword      `json:"swords"`
	Badges      []Badge      `json:"badges"`
	SwordLadder []RankStep   `json:"sword_ladder"`
	SmithLadder []smith.Step `json:"smith_ladder"`
	RankUp      RankUps      `json:"rank_up"`
}

// UpdateForge recomputes the user's swords, badges and experience and raises what they reached. Nothing is lowered.
func (s *Service) UpdateForge(ctx context.Context, userID int64) (*ForgeView, error) {
	st, err := s.state()
	if err != nil {
		return nil, err
	}
	var email, name string
	if err := s.DB.QueryRow(ctx, `SELECT email, name FROM users WHERE id = $1`, userID).Scan(&email, &name); err != nil {
		return nil, err
	}
	v := &ForgeView{SwordLadder: Ladder(st.Platform.Settings.Ranks), SmithLadder: smith.Ladder(st.Platform.Settings.Smith),
		Swords: []Sword{}, Badges: []Badge{}, RankUp: RankUps{Swords: []SwordUp{}}}
	// No smiths row yet: this is the person's first computation, and it is silent.
	var first bool
	if err := s.DB.QueryRow(ctx, `SELECT NOT EXISTS (SELECT 1 FROM smiths WHERE user_id = $1)`, userID).Scan(&first); err != nil {
		return nil, err
	}
	titles := map[string]string{}
	work := 0.0
	for _, e := range (rbac.Checker{P: st.Platform}).Enrollments(email) {
		sd, err := s.Standing(ctx, userID, e.Team.ID, e.Program.Training)
		if err != nil {
			return nil, err
		}
		if sd == nil || sd.Total == 0 {
			continue // ponytail: unavailable content neither helps nor hurts until it syncs again
		}
		id := sd.Training.ID
		titles[id] = sd.Training.Title
		work += sd.Done * smith.PerPoint
		pct := forgePercent(sd.Done, sd.Total)
		// A new row is shown unless it is the first computation or only Ore; an existing one is raised, never lowered.
		lvl := RankFor(pct, v.SwordLadder)
		if _, err := s.DB.Exec(ctx, `INSERT INTO sword_ranks (user_id, training, level, seen) VALUES ($1, $2, $3, $4)
			ON CONFLICT (user_id, training) DO UPDATE SET level = EXCLUDED.level, earned_at = now(), seen = false
			WHERE sword_ranks.level < EXCLUDED.level`, userID, id, lvl, first || lvl == 0); err != nil {
			return nil, err
		}
		v.Swords = append(v.Swords, Sword{Training: id, Title: sd.Training.Title, Percent: pct})
		if sd.Done >= sd.Total-1e-9 {
			if _, err := s.DB.Exec(ctx, `INSERT INTO badges (user_id, training, team) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
				userID, id, e.Team.ID); err != nil {
				return nil, err
			}
		}
	}
	// Stored swords, the ones of trainings no longer taken included: their milestones are kept.
	rows, err := s.DB.Query(ctx, `SELECT training, level, NOT seen FROM sword_ranks WHERE user_id = $1 ORDER BY training`, userID)
	if err != nil {
		return nil, err
	}
	stored, milestones := map[string]int{}, 0
	var tr string
	var lvl int
	var unseen bool
	if _, err := pgx.ForEachRow(rows, []any{&tr, &lvl, &unseen}, func() error {
		lvl = min(lvl, len(v.SwordLadder)-1)
		stored[tr], milestones = lvl, milestones+smith.MilestoneXP(lvl)
		if unseen && titles[tr] != "" {
			v.RankUp.Swords = append(v.RankUp.Swords, SwordUp{Training: tr, Title: titles[tr], Level: lvl, Rank: v.SwordLadder[lvl].Name})
		}
		return nil
	}); err != nil {
		return nil, err
	}
	for i := range v.Swords {
		sw := &v.Swords[i]
		sw.Level = stored[sw.Training] // clamped above
		sw.Rank = v.SwordLadder[sw.Level].Name
		if sw.Level+1 < len(v.SwordLadder) {
			sw.Next = &v.SwordLadder[sw.Level+1]
		}
	}
	xp := int(math.Round(work)) + milestones
	if _, err := s.DB.Exec(ctx, `INSERT INTO smiths (user_id, xp, level, seen) VALUES ($1, $2, $3, true)
		ON CONFLICT (user_id) DO UPDATE SET xp = EXCLUDED.xp WHERE smiths.xp < EXCLUDED.xp`, userID, xp, smith.LevelFor(xp, v.SmithLadder)); err != nil {
		return nil, err
	}
	if err := s.DB.QueryRow(ctx, `SELECT xp FROM smiths WHERE user_id = $1`, userID).Scan(&v.Smith.XP); err != nil {
		return nil, err
	}
	// The row exists now (inserted seen, silently, on the first computation): from here the level only rises, and the
	// one caller whose update returns a row announces it.
	var raisedTo int
	err = s.DB.QueryRow(ctx, `UPDATE smiths SET level = $2, earned_at = now(), seen = false WHERE user_id = $1 AND level < $2 RETURNING level`,
		userID, smith.LevelFor(v.Smith.XP, v.SmithLadder)).Scan(&raisedTo)
	raised := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if err := s.DB.QueryRow(ctx, `SELECT level, NOT seen FROM smiths WHERE user_id = $1`, userID).Scan(&v.Smith.Level, &unseen); err != nil {
		return nil, err
	}
	ref := smith.RefFor(v.Smith.Level)
	v.Smith.Title = ref.Title
	if v.Smith.Level+1 < len(v.SmithLadder) {
		v.Smith.Next = &v.SmithLadder[v.Smith.Level+1]
	}
	if unseen {
		v.RankUp.Smith = &ref
	}
	if raised {
		s.rankUp(ctx, email, name, smith.RefFor(raisedTo).Title)
	}
	rows, err = s.DB.Query(ctx, `SELECT training, earned_at FROM badges WHERE user_id = $1 ORDER BY earned_at`, userID)
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

func (s *Service) rankUp(ctx context.Context, email, name, title string) {
	if s.Notify == nil {
		return
	}
	who := name
	if who == "" {
		who = email
	}
	a := smith.Article(title)
	ev := notify.Event{Kind: notify.RankUp, To: []string{email}, Subject: who + " is now " + a + " " + title,
		Text: who + " is now " + a + " " + title + " of the Crucible. The forge remembers.", Link: "/"}
	if err := s.Notify.Notify(context.WithoutCancel(ctx), ev); err != nil {
		slog.Error("queueing the rank-up notification failed", "err", err)
	}
}

// SeenRankUp clears one banner, only at the level the client was shown; a rank raised since stays unseen.
func (s *Service) SeenRankUp(ctx context.Context, userID int64, kind, training string, level int) error {
	switch kind {
	case "smith":
		_, err := s.DB.Exec(ctx, `UPDATE smiths SET seen = true WHERE user_id = $1 AND level = $2`, userID, level)
		return err
	case "sword":
		_, err := s.DB.Exec(ctx, `UPDATE sword_ranks SET seen = true WHERE user_id = $1 AND training = $2 AND level = $3`, userID, training, level)
		return err
	}
	return apperr.Wrap(apperr.Invalid, `kind must be "smith" or "sword"`)
}

// SwordRanks are these people's sword levels by training, for Journey.
func (s *Service) SwordRanks(ctx context.Context, userIDs []int64) (map[int64]map[string]int, error) {
	rows, err := s.DB.Query(ctx, `SELECT user_id, training, level FROM sword_ranks WHERE user_id = ANY($1)`, userIDs)
	if err != nil {
		return nil, err
	}
	out := map[int64]map[string]int{}
	var id int64
	var tr string
	var lvl int
	_, err = pgx.ForEachRow(rows, []any{&id, &tr, &lvl}, func() error {
		if out[id] == nil {
			out[id] = map[string]int{}
		}
		out[id][tr] = lvl
		return nil
	})
	return out, err
}
