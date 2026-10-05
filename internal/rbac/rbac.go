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

	// Check self-rule first (spec: nobody scores or approves for themselves)
	if (a == Score || a == ApproveLabs) && subject == actor {
		return false
	}

	if c.IsAdmin(actor) {
		return true
	}

	t := c.P.Teams[team]
	if t == nil {
		return false
	}

	role := t.RoleOf(actor)
	p := t.Programs[training]

	// nil-safe: use empty program if training doesn't exist (empty roles/enrollment)
	if p == nil {
		p = &config.Program{}
	}

	in := func(list []string) bool { return slices.Contains(list, actor) }

	switch a {
	case TakeTraining:
		return in(p.Enrolled)
	case ViewProgress:
		return (subject == actor && in(p.Enrolled)) || role == "leader" || role == "senior" ||
			in(p.Roles.Manager) || in(p.Roles.Scorers) || (subject != "" && t.Mentors[subject] == actor)
	case ManageProgram:
		return role == "leader" || in(p.Roles.Manager)
	case Score:
		return in(p.Roles.Scorers)
	case ApproveLabs:
		return role == "leader" || in(p.Roles.Approvers)
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

// Approval tiers, lowest first (spec §9.1).
const (
	TierAuto     = "auto"
	TierApprover = "approver"
	TierLeader   = "leader"
	TierAdmin    = "admin"
)

// Tier routes a lab request by its estimate: local/cluster at or under auto_approve_usd start at once (aws never
// does); up to tier1 a program approver decides; up to tier2 the team leader; above that an admin.
func Tier(runtime string, estimateUSD float64, t config.CostTiers) string {
	switch {
	case runtime != "aws" && estimateUSD <= t.AutoApproveUSD:
		return TierAuto
	case estimateUSD <= t.Tier1USD:
		return TierApprover
	case estimateUSD <= t.Tier2USD:
		return TierLeader
	}
	return TierAdmin
}

// NextTier is one escalation step: approver → leader → admin → "" (nobody left: the request expires).
func NextTier(tier string) string {
	switch tier {
	case TierApprover:
		return TierLeader
	case TierLeader:
		return TierAdmin
	}
	return ""
}

// TierApprovers lists who decides at a tier, never the requester. Used to route and to notify.
func (c Checker) TierApprovers(tier, team, training, requester string) []string {
	requester = strings.ToLower(requester)
	var list []string
	t := c.P.Teams[team]
	switch {
	case tier == TierApprover && t != nil && t.Programs[training] != nil:
		list = t.Programs[training].Roles.Approvers
	case tier == TierLeader && t != nil:
		list = []string{t.Leader}
	case tier == TierAdmin:
		list = c.P.Admins
	}
	return slices.DeleteFunc(slices.Clone(list), func(e string) bool { return e == requester || e == "" })
}

// Route returns the first tier at or above tier with someone other than the requester to decide. Admin is the
// last stop even when it is empty; such a request expires unanswered.
func (c Checker) Route(tier, team, training, requester string) string {
	for t := tier; t != ""; t = NextTier(t) {
		if t == TierAdmin || len(c.TierApprovers(t, team, training, requester)) > 0 {
			return t
		}
	}
	return TierAdmin
}

// MayApprove: admins approve any amount and over-cap overrides; the team leader up to tier2; program approvers up
// to tier1; nobody their own request (spec §5.3). Eligibility follows the amount, not the request's current tier:
// escalation adds deciders, it never removes them.
func (c Checker) MayApprove(actor, requester, team, training string, estimateUSD float64, overCap bool) bool {
	actor = strings.ToLower(actor)
	if actor == strings.ToLower(requester) {
		return false
	}
	if c.IsAdmin(actor) {
		return true
	}
	tiers, t := c.P.Settings.CostTiers, c.P.Teams[team]
	if overCap || tiers == nil || t == nil {
		return false
	}
	if t.Leader == actor {
		return estimateUSD <= tiers.Tier2USD
	}
	if p := t.Programs[training]; p != nil && slices.Contains(p.Roles.Approvers, actor) {
		return estimateUSD <= tiers.Tier1USD
	}
	return false
}
