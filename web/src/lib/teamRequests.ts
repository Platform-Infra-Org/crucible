import type { ProgramConfig, TeamView } from '../types'
import { parseEmails, parseMentors } from './lists'

// The requests the team and program pages send. With the configuration in Postgres (`config_in_db` on /api/me) they
// go to /api/org and echo the version the page read, so the server can answer 409 when someone saved first. With a
// git platform repo they go to the git-backed routes and echo the platform commit instead.

export const teamPath = (inDB: boolean, id: string) => (inDB ? `/api/org/teams/${id}` : `/api/teams/${id}`)

export const rosterRequest = (inDB: boolean, t: TeamView, f: { seniors: string; members: string; trainees: string; mentors: string }) => {
  const lists = { seniors: parseEmails(f.seniors), members: parseEmails(f.members), trainees: parseEmails(f.trainees), mentors: parseMentors(f.mentors) }
  return {
    path: `${teamPath(inDB, t.id)}/roster`,
    json: inDB ? { version: t.version, name: t.name, leader: t.leader, ...lists } : { base_sha: t.platform_sha, ...lists },
  }
}

export const budgetRequest = (inDB: boolean, t: TeamView, monthly: string, cap: string) => {
  const amounts = { monthly_usd: Number(monthly) || 0, hard_cap_usd: Number(cap) || 0 }
  return { path: `${teamPath(inDB, t.id)}/budget`, json: inDB ? { version: t.budget?.version ?? 0, ...amounts } : { base_sha: t.platform_sha, ...amounts } }
}

// A new program: POST in the database (the server creates it at version 1), PUT in git.
export const enrollRequest = (inDB: boolean, t: TeamView, training: string) => ({
  path: `${teamPath(inDB, t.id)}/programs/${training}`,
  method: inDB ? 'POST' : 'PUT',
  json: { ...(inDB ? {} : { base_sha: t.platform_sha }), enrolled: [], roles: { manager: [], scorers: [], approvers: [] }, schedule: '',
    ...(inDB ? { ttl: '', idle_timeout: '', max_extension: '' } : { lab_defaults: { ttl: '', idle_timeout: '', max_extension: '' } }),
    budget_usd_month: 0 },
})

export const pinRequest = (inDB: boolean, t: TeamView, training: string, sha: string) => ({
  path: `${teamPath(inDB, t.id)}/programs/${training}/pin`,
  json: inDB ? { sha } : { base_sha: t.platform_sha, ref: sha },
})

export const programRequest = (inDB: boolean, t: TeamView, p: ProgramConfig, f: {
  enrolled: Iterable<string>; managers: string; scorers: string; approvers: string; schedule: string
  ttl: string; idle: string; ext: string; budget: string; reviewSelf: boolean
}) => {
  const roles = { manager: parseEmails(f.managers), scorers: parseEmails(f.scorers), approvers: parseEmails(f.approvers) }
  const common = { enrolled: [...f.enrolled], roles, schedule: f.schedule, budget_usd_month: Number(f.budget) || 0, review_self_reported: f.reviewSelf }
  return {
    path: `${teamPath(inDB, t.id)}/programs/${p.training}`,
    json: inDB
      ? { version: p.version, ...common, ttl: f.ttl, idle_timeout: f.idle, max_extension: f.ext,
          // Windows written in git stay unless a named schedule replaces them.
          ...(f.schedule === '' && p.inline_schedule ? { inline_schedule: p.inline_schedule } : {}) }
      : { base_sha: t.platform_sha, ...common, lab_defaults: { ttl: f.ttl, idle_timeout: f.idle, max_extension: f.ext } },
  }
}
