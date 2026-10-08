import type { ProgramConfig, Roles } from '../types'
import { parseEmails, parseMentors } from './lists'

// The requests the team and trainings pages send to /api/org. Each echoes the version the page read, so the server
// answers 409 when someone saved first.

// Roster is the part of a team a roster save needs: the team page's TeamView and the trainings page's TeamRoster both fit.
export type Roster = {
  id: string; name: string; version?: number; leader: string; seniors: string[]; members: string[]; trainees: string[]
  mentors: Record<string, string>
}

export const teamPath = (id: string) => `/api/org/teams/${id}`
const programPath = (team: string, training: string) => `${teamPath(team)}/programs/${training}`

export const rosterRequest = (t: Roster, f: { seniors: string; members: string; trainees: string; mentors: string }) => ({
  path: `${teamPath(t.id)}/roster`,
  json: { version: t.version, name: t.name, leader: t.leader, seniors: parseEmails(f.seniors), members: parseEmails(f.members),
    trainees: parseEmails(f.trainees), mentors: parseMentors(f.mentors) },
})

// addTraineeRequest saves the roster with one more trainee: how someone new joins a team to be enrolled.
export const addTraineeRequest = (t: Roster, email: string) => ({
  path: `${teamPath(t.id)}/roster`,
  json: { version: t.version, name: t.name, leader: t.leader, seniors: t.seniors, members: t.members,
    trainees: [...t.trainees, email.trim().toLowerCase()], mentors: t.mentors },
})

export const budgetRequest = (t: { id: string; budget?: { version: number } }, monthly: string, cap: string) => ({
  path: `${teamPath(t.id)}/budget`,
  json: { version: t.budget?.version ?? 0, monthly_usd: Number(monthly) || 0, hard_cap_usd: Number(cap) || 0 },
})

// A new program: the server creates it at version 1 with nobody enrolled and the default roles.
export const enrollRequest = (team: string, training: string) => ({
  path: programPath(team, training),
  method: 'POST',
  json: { enrolled: [], roles: { manager: [], scorers: [], approvers: [] }, schedule: '', ttl: '', idle_timeout: '', max_extension: '',
    budget_usd_month: 0 },
})

export const pinRequest = (team: string, training: string, sha: string) => ({ path: `${programPath(team, training)}/pin`, json: { sha } })

export type ProgramFields = {
  enrolled: Iterable<string>; roles: Roles; schedule: string; ttl: string; idle: string; ext: string; budget: string; reviewSelf: boolean
}

// programFields is a program as it stands, the starting point for any change to it.
export const programFields = (p: ProgramConfig): ProgramFields => ({
  enrolled: p.enrolled, roles: p.roles, schedule: p.schedule, ttl: p.lab_defaults.ttl, idle: p.lab_defaults.idle_timeout,
  ext: p.lab_defaults.max_extension, budget: String(p.budget_usd_month), reviewSelf: p.review_self_reported,
})

// programRequest replaces the whole program, as the server's PUT does. Roles are the stored ones (an empty list means
// the default), never the effective ones, which would freeze today's leader and seniors into the program.
export const programRequest = (team: string, p: ProgramConfig, f: ProgramFields) => ({
  path: programPath(team, p.training),
  json: { version: p.version, enrolled: [...f.enrolled], roles: f.roles, schedule: f.schedule, budget_usd_month: Number(f.budget) || 0,
    review_self_reported: f.reviewSelf, ttl: f.ttl, idle_timeout: f.idle, max_extension: f.ext,
    // Inline windows stay unless a named schedule replaces them.
    ...(f.schedule === '' && p.inline_schedule ? { inline_schedule: p.inline_schedule } : {}) },
})

// programChange is programRequest for one change, everything else kept as it stands.
export const programChange = (team: string, p: ProgramConfig, change: Partial<ProgramFields>) =>
  programRequest(team, p, { ...programFields(p), ...change })

export const people = (t: Roster) => [t.leader, ...t.seniors, ...t.members, ...t.trainees]

// enrollPlan decides how to enroll email in a program of team t: already enrolled, enroll a team member, add them to
// the team as a trainee first (only someone who can edit the team), or refuse with the reason.
export function enrollPlan(t: Roster & { can_edit_team: boolean }, p: ProgramConfig, raw: string):
  { kind: 'enroll' | 'add-and-enroll'; email: string } | { kind: 'refuse'; reason: string } {
  const email = raw.trim().toLowerCase()
  if (!/^[^\s@]+@[^\s@]+$/.test(email)) return { kind: 'refuse', reason: 'Enter an email address, such as someone@example.com.' }
  if (p.enrolled.includes(email)) return { kind: 'refuse', reason: `${email} is already enrolled.` }
  if (people(t).includes(email)) return { kind: 'enroll', email }
  if (t.can_edit_team) return { kind: 'add-and-enroll', email }
  return { kind: 'refuse', reason: `${email} is not on ${t.name}. Ask the team leader or an admin to add them first.` }
}
