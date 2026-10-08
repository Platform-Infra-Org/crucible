import { describe, expect, test, vi } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router'
import { ApiError } from '../api'
import { Conflict, reportSaveError } from '../components/Conflict'
import { addTraineeRequest, budgetRequest, enrollPlan, enrollRequest, pinRequest, programChange, programFields, programRequest, rosterRequest, teamPath } from '../lib/teamRequests'
import type { ProgramConfig, TeamView } from '../types'
import { CreateTeam, TeamPage } from './Team'

const prog: ProgramConfig = {
  training: 'forge-101', title: 'Forge 101', version: 7, enrolled: ['a@x'], roles: { manager: ['l@x'], scorers: [], approvers: [] },
  schedule: '', lab_defaults: { ttl: '', idle_timeout: '', max_extension: '' }, budget_usd_month: 0, review_self_reported: false,
  can_manage: true, running_sha: '', head_sha: '', pinned_ref: '',
}
const team: TeamView = {
  id: 'platform', name: 'Platform', leader: 'l@x', seniors: [], members: [], trainees: ['a@x'], mentors: {}, version: 3,
  budget: { version: 5, monthly_usd: 10, hard_cap_usd: 20 }, programs: [prog], available_trainings: [], schedules: [],
  can_edit_team: true, is_admin: true,
}
vi.mock('react-router', async (orig) => ({ ...(await orig<typeof import('react-router')>()), useParams: () => ({ team: 'platform', training: 'forge-101' }) }))
const fetched: string[] = []
vi.mock('../useFetch', () => ({ useFetch: (path: string) => { fetched.push(path); return { data: team, error: undefined, reload: () => {} } } }))
vi.mock('../me', () => ({ useMe: () => ({ me: { is_admin: true, can_manage_trainings: true } }) }))

describe('requests', () => {
  test('go to /api/org and echo the version the page read', () => {
    const r = rosterRequest(team, { seniors: '', members: '', trainees: 'a@x', mentors: '' })
    expect(r.path).toBe('/api/org/teams/platform/roster')
    expect(r.json).toMatchObject({ version: 3, name: 'Platform', leader: 'l@x', trainees: ['a@x'] })
    expect(budgetRequest(team, '10', '').json).toEqual({ version: 5, monthly_usd: 10, hard_cap_usd: 0 })
    expect(budgetRequest({ ...team, budget: undefined }, '4', '').json).toMatchObject({ version: 0 }) // no budget row yet
    const p = programRequest('platform', prog, programFields(prog))
    expect(p.path).toBe('/api/org/teams/platform/programs/forge-101')
    expect(p.json).toMatchObject({ version: 7, enrolled: ['a@x'], ttl: '', roles: { manager: ['l@x'] } })
    expect(enrollRequest('platform', 'net-101')).toMatchObject({ method: 'POST', path: '/api/org/teams/platform/programs/net-101' })
    expect(pinRequest('platform', 'forge-101', 'abc')).toEqual({ path: '/api/org/teams/platform/programs/forge-101/pin', json: { sha: 'abc' } })
    expect(teamPath('platform')).toBe('/api/org/teams/platform')
  })
  test('one change keeps everything else as it stands', () => {
    const p = { ...prog, schedule: 'nights', budget_usd_month: 25, review_self_reported: true, lab_defaults: { ttl: '2h', idle_timeout: '30m', max_extension: '' } }
    expect(programChange('platform', p, { enrolled: ['a@x', 'b@x'] }).json).toEqual({ version: 7, enrolled: ['a@x', 'b@x'],
      roles: p.roles, schedule: 'nights', budget_usd_month: 25, review_self_reported: true, ttl: '2h', idle_timeout: '30m', max_extension: '' })
  })
  test('roles sent are the stored ones, never the effective defaults', () => {
    const p = { ...prog, roles: { manager: [], scorers: [], approvers: [] }, effective_roles: { manager: ['l@x'], scorers: ['s@x'], approvers: ['l@x'] } }
    expect(programChange('platform', p, { enrolled: [] }).json.roles).toEqual({ manager: [], scorers: [], approvers: [] })
  })
  test('inline windows are kept unless a named schedule replaces them', () => {
    const inl = { ...prog, inline_schedule: { timezone: 'UTC' } }
    expect(programChange('platform', inl, { schedule: '' }).json).toHaveProperty('inline_schedule')
    expect(programChange('platform', inl, { schedule: 'nights' }).json).not.toHaveProperty('inline_schedule')
  })
  test('adding a trainee keeps the rest of the roster', () => {
    expect(addTraineeRequest({ ...team, seniors: ['s@x'], mentors: { 'a@x': 's@x' } }, ' New@X ').json).toEqual({ version: 3, name: 'Platform',
      leader: 'l@x', seniors: ['s@x'], members: [], trainees: ['a@x', 'new@x'], mentors: { 'a@x': 's@x' } })
  })
})

describe('enrolling someone', () => {
  const t = { ...team, seniors: ['s@x'], trainees: ['a@x', 'b@x'] }
  test('a team member is enrolled; someone already enrolled is refused', () => {
    expect(enrollPlan(t, prog, ' B@x ')).toEqual({ kind: 'enroll', email: 'b@x' })
    expect(enrollPlan(t, prog, 'a@x')).toMatchObject({ kind: 'refuse', reason: 'a@x is already enrolled.' })
  })
  test('someone new joins the team first, but only when you may edit the team', () => {
    expect(enrollPlan(t, prog, 'new@x')).toEqual({ kind: 'add-and-enroll', email: 'new@x' })
    expect(enrollPlan({ ...t, can_edit_team: false }, prog, 'new@x')).toMatchObject({ kind: 'refuse', reason: expect.stringMatching(/not on Platform/) })
  })
  test('what is not an email address is refused before anything is sent', () => {
    expect(enrollPlan(t, prog, 'bob')).toMatchObject({ kind: 'refuse' })
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
  test('team page reads /api/org, has a labelled save for each form, and sends programs to the trainings page', () => {
    fetched.length = 0
    const h = renderToStaticMarkup(<MemoryRouter><TeamPage /></MemoryRouter>)
    expect(fetched).toEqual(['/api/org/teams/platform'])
    expect(h).toContain('Save roster')
    expect(h).toContain('Save budget')
    expect(h).toContain('href="/trainings/manage/forge-101#team-platform"')
    expect(h).toContain('Start a training for this team')
    expect(h).not.toContain('Enroll the team')
  })
  test('create-team has labelled id, name and leader', () => {
    const h = renderToStaticMarkup(<MemoryRouter><CreateTeam /></MemoryRouter>)
    for (const l of ['Team id', 'Name', "Leader email", 'Create team']) expect(h).toContain(l)
  })
})
