import { describe, expect, test, vi } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router'
import { ApiError } from '../api'
import { Conflict, reportSaveError } from '../components/Conflict'
import { budgetRequest, pinRequest, programChange, programFields, programRequest, retag, rosterRequest, teamPath } from '../lib/teamRequests'
import type { ProgramConfig, TeamView } from '../types'
import { CreateTeam, TeamPage, TeamsIndex } from './Team'

const prog: ProgramConfig = {
  training: 'forge-101', title: 'Forge 101', team: 'platform', version: 7,
  roles: { participants: ['platform_trainees'], editors: ['l@x'], admins: [] }, resolved: { participants: ['a@x'], editors: ['l@x'], admins: [] },
  schedule: '', lab_defaults: { ttl: '', idle_timeout: '', max_extension: '' }, budget_usd_month: 0, review_self_reported: false,
  can_manage: true, running_sha: '', head_sha: '', pinned_ref: '',
}
const team: TeamView = {
  id: 'platform', name: 'Platform', members: [{ email: 'l@x', tag: 'leader' }, { email: 'a@x', tag: 'trainee' }], mentors: {}, version: 3,
  budget: { version: 5, monthly_usd: 10, hard_cap_usd: 20 }, programs: [prog], schedules: [], can_edit_team: true, is_admin: true,
}
vi.mock('react-router', async (orig) => ({ ...(await orig<typeof import('react-router')>()), useParams: () => ({ team: 'platform', training: 'forge-101' }) }))
const fetched: string[] = []
const teams = [{ id: 'platform', name: 'Platform', role: 'leader' }, { id: 'apps', name: 'Apps', role: 'admin' }]
const people = { people: [{ email: 'mover@x', name: 'Mo', team: 'apps', tag: 'senior' }], groups: ['all'] }
vi.mock('../useFetch', () => ({
  useFetch: (path: string | null) => {
    if (path) fetched.push(path)
    return { data: path === '/api/teams' ? teams : path === '/api/org/people' ? people : path ? team : undefined, error: undefined, reload: () => {} }
  },
}))
vi.mock('../me', () => ({ useMe: () => ({ me: { is_admin: true, can_manage_trainings: true } }) }))

describe('requests', () => {
  test('go to /api/org and echo the version the page read', () => {
    const r = rosterRequest(team, {})
    expect(r.path).toBe('/api/org/teams/platform/roster')
    expect(r.json).toEqual({ version: 3, name: 'Platform', members: { 'l@x': 'leader', 'a@x': 'trainee' }, mentors: {} })
    expect(budgetRequest(team, '10', '').json).toEqual({ version: 5, monthly_usd: 10, hard_cap_usd: 0 })
    expect(budgetRequest({ ...team, budget: undefined }, '4', '').json).toMatchObject({ version: 0 }) // no budget row yet
    const p = programRequest(prog, programFields(prog))
    expect(p.path).toBe('/api/org/teams/platform/programs/forge-101')
    expect(p.json).toMatchObject({ version: 7, ttl: '', roles: { participants: ['platform_trainees'] } })
    expect(pinRequest('platform', 'forge-101', 'abc')).toEqual({ path: '/api/org/teams/platform/programs/forge-101/pin', json: { sha: 'abc' } })
    expect(teamPath('platform')).toBe('/api/org/teams/platform')
  })
  test('one change keeps everything else as it stands', () => {
    const p = { ...prog, schedule: 'nights', budget_usd_month: 25, review_self_reported: true, lab_defaults: { ttl: '2h', idle_timeout: '30m', max_extension: '' } }
    const roles = { participants: ['all_trainees'], editors: [], admins: [] }
    expect(programChange(p, { roles }).json).toEqual({ version: 7, roles, schedule: 'nights', budget_usd_month: 25, review_self_reported: true,
      ttl: '2h', idle_timeout: '30m', max_extension: '' })
  })
  test('roles go back as given, groups and all, never as resolved', () => {
    expect(programChange(prog, { budget: '3' }).json.roles).toEqual(prog.roles)
  })
  test('inline windows are kept unless a named schedule replaces them', () => {
    const inl = { ...prog, inline_schedule: { timezone: 'UTC' } }
    expect(programChange(inl, { schedule: '' }).json).toHaveProperty('inline_schedule')
    expect(programChange(inl, { schedule: 'nights' }).json).not.toHaveProperty('inline_schedule')
  })
})

describe('changing a tag', () => {
  test('a member gets any tag, leader included', () => {
    expect(retag(team, ' A@X ', 'leader')).toEqual({ members: { 'l@x': 'leader', 'a@x': 'leader' } })
    expect(retag(team, 'a@x', 'junior')).toEqual({ members: { 'l@x': 'leader', 'a@x': 'junior' } })
  })
  test('only people on the team, and the team keeps a leader', () => {
    expect(retag(team, 'stranger@x', 'senior')).toMatchObject({ problem: expect.stringMatching(/not on Platform/) })
    expect(retag(team, 'l@x', 'senior')).toMatchObject({ problem: expect.stringMatching(/needs a leader/) })
  })
})

describe('a stale save', () => {
  test('409 is reported as a conflict, not a raw error', () => {
    expect(reportSaveError(new ApiError(409, 'someone changed this team, reload and try again'))).toBe(true)
  })
  test('the reload notice reads plainly', () => {
    const h = renderToStaticMarkup(<Conflict onReload={() => {}} message="Someone changed this team, reload to see the latest." />)
    expect(h).toContain('Someone changed this team, reload')
    expect(h).toContain('role="alert"')
  })
})

describe('pages', () => {
  test('team page: members by tag, the tag changer autocompletes the team, admins add people from everyone', () => {
    fetched.length = 0
    const h = renderToStaticMarkup(<MemoryRouter><TeamPage /></MemoryRouter>)
    expect(fetched).toEqual(['/api/org/teams/platform', '/api/org/people'])
    expect(h).toContain('Led by l@x')
    expect(h).toContain('aria-label="Leaders"')
    expect(h).toMatch(/<datalist id="members-platform"><option value="l@x">leader<\/option><option value="a@x">trainee<\/option><\/datalist>/)
    expect(h).toContain('Set tag')
    expect(h).toContain('Add to team')
    expect(h).toContain('<option value="mover@x">Mo · senior on apps</option>')
    expect(h).toContain('aria-label="Take a@x off the team"')
    expect(h).toContain('Save budget')
    expect(h).toContain('href="/trainings/manage/forge-101"')
    expect(h).toContain('Trainings it owns')
    expect(h).toContain('Delete team') // admins only: the server refuses everyone else
    team.is_admin = false
    fetched.length = 0
    const leader = renderToStaticMarkup(<MemoryRouter><TeamPage /></MemoryRouter>)
    team.is_admin = true
    expect(fetched).toEqual(['/api/org/teams/platform']) // a leader picks only from the team
    expect(leader).toContain('Set tag')
    for (const admin of ['Delete team', 'Add to team', 'Take a@x off']) expect(leader).not.toContain(admin)
  })
  test('the team list shows cards and keeps the create form behind + Start a team', () => {
    const h = renderToStaticMarkup(<MemoryRouter><TeamsIndex /></MemoryRouter>)
    expect(h).toContain('href="/teams/platform"')
    expect(h.indexOf('Apps')).toBeLessThan(h.indexOf('Platform')) // sorted by name
    expect(h).toContain('Start a team')
    expect(h).not.toContain('Create team')
  })
  test('create-team has labelled id, name and leader', () => {
    const h = renderToStaticMarkup(<MemoryRouter><CreateTeam /></MemoryRouter>)
    for (const l of ['Team id', 'Name', 'Leader email', 'Create team']) expect(h).toContain(l)
  })
})
