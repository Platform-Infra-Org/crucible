import type { Access, Member, ProgramConfig, Tag } from '../types'

// The requests the team and trainings pages send to /api/org. Each echoes the version the page read, so the server
// answers 409 when someone saved first.

export const TAGS: Tag[] = ['leader', 'senior', 'junior', 'trainee']
export const tagPlural: Record<Tag, string> = { leader: 'Leaders', senior: 'Seniors', junior: 'Juniors', trainee: 'Trainees' }

export const teamPath = (id: string) => `/api/org/teams/${id}`
const programPath = (team: string, training: string) => `${teamPath(team)}/programs/${training}`

type Roster = { id: string; name: string; version: number; members: Member[] }

export const membersOf = (t: { members: Member[] }) => Object.fromEntries(t.members.map((m) => [m.email, m.tag])) as Record<string, Tag>

// rosterRequest saves the team with its members (email → tag); the name as it stands.
export const rosterRequest = (t: Roster, members: Record<string, Tag>) => ({
  path: `${teamPath(t.id)}/roster`,
  json: { version: t.version, name: t.name, members },
})

// retag gives email a new tag, or explains why not: only people already on the team, and a team keeps a leader.
export function retag(t: Roster, raw: string, tag: Tag): { members: Record<string, Tag> } | { problem: string } {
  const email = raw.trim().toLowerCase()
  const members = membersOf(t)
  if (!members[email]) return { problem: `${email || 'That'} is not on ${t.name}. Pick someone from the team.` }
  members[email] = tag
  if (!Object.values(members).includes('leader')) return { problem: `${t.name} needs a leader. Make someone else leader first.` }
  return { members }
}

export const budgetRequest = (t: { id: string; budget?: { version: number } }, monthly: string, cap: string) => ({
  path: `${teamPath(t.id)}/budget`,
  json: { version: t.budget?.version ?? 0, monthly_usd: Number(monthly) || 0, hard_cap_usd: Number(cap) || 0 },
})

export const pinRequest = (team: string, training: string, sha: string) => ({ path: `${programPath(team, training)}/pin`, json: { sha } })

export type ProgramFields = {
  roles: Access; schedule: string; ttl: string; idle: string; ext: string; budget: string; reviewSelf: boolean
}

// programFields is a training's run as it stands, the starting point for any change to it.
export const programFields = (p: ProgramConfig): ProgramFields => ({
  roles: p.roles, schedule: p.schedule, ttl: p.lab_defaults.ttl, idle: p.lab_defaults.idle_timeout,
  ext: p.lab_defaults.max_extension, budget: String(p.budget_usd_month), reviewSelf: p.review_self_reported,
})

// programRequest replaces the whole run, as the server's PUT does. Roles are sent as given (groups stay groups), never
// as resolved, which would freeze today's members into the training.
export const programRequest = (p: ProgramConfig, f: ProgramFields) => ({
  path: programPath(p.team, p.training),
  json: { version: p.version, roles: f.roles, schedule: f.schedule, budget_usd_month: Number(f.budget) || 0,
    review_self_reported: f.reviewSelf, ttl: f.ttl, idle_timeout: f.idle, max_extension: f.ext,
    // Inline windows stay unless a named schedule replaces them.
    ...(f.schedule === '' && p.inline_schedule ? { inline_schedule: p.inline_schedule } : {}) },
})

// programChange is programRequest for one change, everything else kept as it stands.
export const programChange = (p: ProgramConfig, change: Partial<ProgramFields>) => programRequest(p, { ...programFields(p), ...change })
