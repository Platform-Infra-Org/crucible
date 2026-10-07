import type { ProgramConfig, TeamView } from '../types'
import { parseEmails, parseMentors } from './lists'

// The bodies the team and program pages send. Every save echoes the version the page read, so the server can
// refuse it (409) when someone else saved first.

export const rosterRequest = (t: TeamView, f: { seniors: string; members: string; trainees: string; mentors: string }) => ({
  path: `/api/org/teams/${t.id}/roster`,
  json: { version: t.version, name: t.name, leader: t.leader, seniors: parseEmails(f.seniors), members: parseEmails(f.members),
    trainees: parseEmails(f.trainees), mentors: parseMentors(f.mentors) },
})

export const budgetRequest = (t: TeamView, monthly: string, cap: string) => ({
  path: `/api/org/teams/${t.id}/budget`,
  json: { version: t.budget?.version ?? 0, monthly_usd: Number(monthly) || 0, hard_cap_usd: Number(cap) || 0 },
})

export const programRequest = (teamId: string, p: ProgramConfig, f: {
  enrolled: Iterable<string>; managers: string; scorers: string; approvers: string; schedule: string
  ttl: string; idle: string; ext: string; budget: string; reviewSelf: boolean
}) => ({
  path: `/api/org/teams/${teamId}/programs/${p.training}`,
  json: { version: p.version, enrolled: [...f.enrolled],
    roles: { manager: parseEmails(f.managers), scorers: parseEmails(f.scorers), approvers: parseEmails(f.approvers) },
    schedule: f.schedule, lab_defaults: { ttl: f.ttl, idle_timeout: f.idle, max_extension: f.ext },
    budget_usd_month: Number(f.budget) || 0, review_self_reported: f.reviewSelf,
    // Windows written in git stay unless a named schedule replaces them.
    ...(f.schedule === '' && p.inline_schedule ? { inline_schedule: p.inline_schedule } : {}) },
})
