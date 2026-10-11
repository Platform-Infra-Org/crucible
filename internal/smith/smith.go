// Package smith is the smith rank (spec 2026-10-11): an experience meter across every training and a ladder about the
// smith's craft. It is a leaf: anything that names a person can look their smith rank up without importing learn.
package smith

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"

	"crucible/internal/config"
)

var Titles = []string{"Bellows Hand", "Apprentice", "Journeyman", "Smith", "Master Smith", "Grandmaster"}

// PerPoint is the experience for each item point done; Milestones[i] the bonus for a sword reaching level i.
const PerPoint = 10

var Milestones = []int{0, 20, 40, 60, 80, 150}

// MilestoneXP is the bonus a sword at this level has earned in all: every milestone up to it.
func MilestoneXP(swordLevel int) int {
	n := 0
	for i := 1; i <= swordLevel && i < len(Milestones); i++ {
		n += Milestones[i]
	}
	return n
}

type Step struct {
	Title string `json:"title"`
	At    int    `json:"at"`
}

func Ladder(t config.SmithThresholds) []Step {
	out := make([]Step, len(Titles))
	for i, at := range t.Steps() {
		out[i] = Step{Title: Titles[i], At: at}
	}
	return out
}

func LevelFor(xp int, ladder []Step) int {
	lvl := 0
	for i, s := range ladder {
		if xp >= s.At {
			lvl = i
		}
	}
	return lvl
}

type Ref struct {
	Level int    `json:"level"`
	Title string `json:"title"`
}

func RefFor(level int) Ref {
	level = max(0, min(level, len(Titles)-1))
	return Ref{Level: level, Title: Titles[level]}
}

// Refs maps a lower-cased email to its smith rank; anyone missing is a Bellows Hand.
type Refs map[string]Ref

func (r Refs) Of(email string) Ref {
	if ref, ok := r[strings.ToLower(email)]; ok {
		return ref
	}
	return RefFor(0)
}

type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// Lookup finds the smith ranks of these people in one query.
func Lookup(ctx context.Context, q Querier, emails []string) (Refs, error) {
	lower := make([]string, 0, len(emails))
	for _, e := range emails {
		if e != "" {
			lower = append(lower, strings.ToLower(e))
		}
	}
	out := Refs{}
	if len(lower) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT lower(u.email), max(s.level) FROM smiths s JOIN users u ON u.id = s.user_id
		WHERE lower(u.email) = ANY($1) GROUP BY lower(u.email)`, lower)
	if err != nil {
		return nil, err
	}
	var e string
	var lvl int
	_, err = pgx.ForEachRow(rows, []any{&e, &lvl}, func() error { out[e] = RefFor(lvl); return nil })
	return out, err
}

// Article is "an" before a vowel sound, else "a" ("an Apprentice", "a Journeyman").
func Article(title string) string {
	if title != "" && strings.ContainsRune("AEIOU", rune(title[0])) {
		return "an"
	}
	return "a"
}
