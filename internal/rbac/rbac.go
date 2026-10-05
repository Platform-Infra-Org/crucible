// Package rbac answers "may this person do this?" from the platform config (spec §5.3).
package rbac

import (
	"slices"
	"sort"
	"strings"

	"crucible/internal/config"
)

type Action int

const (
	TakeTraining Action = iota
	ViewProgress
	ManageProgram
	Score
	ApproveLabs
	ViewSpend
	EditTeam
)

type Checker struct{ P *config.Platform }

type Enrollment struct {
	Team    *config.Team
	Program *config.Program
}

func (c Checker) IsAdmin(email string) bool {
	return slices.Contains(c.P.Admins, strings.ToLower(email))
}

func (c Checker) Can(actor string, a Action, team, training, subject string) bool {
	actor, subject = strings.ToLower(actor), strings.ToLower(subject)
	if c.IsAdmin(actor) {
		return true
	}
	t := c.P.Teams[team]
	if t == nil {
		return false
	}
	role := t.RoleOf(actor)
	p := t.Programs[training]
	in := func(list []string) bool { return p != nil && slices.Contains(list, actor) }

	switch a {
	case TakeTraining:
		return in(p.Enrolled)
	case ViewProgress:
		return (subject == actor && in(p.Enrolled)) || role == "leader" || role == "senior" ||
			in(p.Roles.Manager) || in(p.Roles.Scorers) || (subject != "" && t.Mentors[subject] == actor)
	case ManageProgram:
		return role == "leader" || in(p.Roles.Manager)
	case Score:
		return subject != actor && in(p.Roles.Scorers)
	case ApproveLabs:
		return subject != actor && (role == "leader" || in(p.Roles.Approvers))
	case ViewSpend:
		return role == "leader" || in(p.Roles.Manager) || in(p.Roles.Approvers)
	case EditTeam:
		return role == "leader"
	}
	return false
}

// Enrollments lists the programs the user is enrolled in, sorted by team then training.
func (c Checker) Enrollments(email string) []Enrollment {
	email = strings.ToLower(email)
	var out []Enrollment
	for _, t := range c.P.Teams {
		for _, p := range t.Programs {
			if slices.Contains(p.Enrolled, email) {
				out = append(out, Enrollment{Team: t, Program: p})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Team.ID != out[j].Team.ID {
			return out[i].Team.ID < out[j].Team.ID
		}
		return out[i].Program.Training < out[j].Program.Training
	})
	return out
}
