import { describe, expect, test, vi } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router'
import { ApiError } from '../api'
import { Conflict, reportSaveError } from '../components/Conflict'
import { budgetRequest, enrollRequest, pinRequest, programRequest, rosterRequest, teamPath } from '../lib/teamRequests'
import type { ProgramConfig, TeamView } from '../types'
import { CreateTeam, TeamPage } from './Team'
import { ProgramSettingsPage } from './ProgramSettings'

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
const meFlag = { inDB: true }
vi.mock('../useFetch', () => ({ useFetch: (path: string) => { fetched.push(path); return { data: team, error: undefined, reload: () => {} } } }))
vi.mock('../me', () => ({ useMe: () => ({ me: { config_in_db: meFlag.inDB, is_admin: true } }) }))

const f = { enrolled: ['a@x'], managers: '', scorers: '', approvers: '', schedule: '', ttl: '', idle: '', ext: '', budget: '', reviewSelf: false }
describe('the endpoint follows config_in_db', () => {
  test('in the database: /api/org, version echoed, no base_sha', () => {
    const r = rosterRequest(true, team, { seniors: '', members: '', trainees: 'a@x', mentors: '' })
    expect(r.path).toBe('/api/org/teams/platform/roster')
    expect(r.json).toMatchObject({ version: 3, name: 'Platform', leader: 'l@x', trainees: ['a@x'] })
    expect(budgetRequest(true, team, '10', '').json).toEqual({ version: 5, monthly_usd: 10, hard_cap_usd: 0 })
    expect(budgetRequest(true, { ...team, budget: undefined }, '4', '').json).toMatchObject({ version: 0 }) // no budget row yet
    const p = programRequest(true, team, prog, f)
    expect(p.path).toBe('/api/org/teams/platform/programs/forge-101')
    expect(p.json).toMatchObject({ version: 7, enrolled: ['a@x'], ttl: '' })
    expect(JSON.stringify(p.json)).not.toContain('base_sha')
    expect(enrollRequest(true, team, 'net-101')).toMatchObject({ method: 'POST', path: '/api/org/teams/platform/programs/net-101' })
    expect(pinRequest(true, team, 'forge-101', 'abc')).toEqual({ path: '/api/org/teams/platform/programs/forge-101/pin', json: { sha: 'abc' } })
  })
  test('in git: the git-backed routes with base_sha', () => {
    const g = { ...team, platform_sha: 'deadbeef' }
    expect(rosterRequest(false, g, { seniors: '', members: '', trainees: '', mentors: '' })).toMatchObject({ path: '/api/teams/platform/roster', json: { base_sha: 'deadbeef' } })
    expect(budgetRequest(false, g, '1', '').path).toBe('/api/teams/platform/budget')
    expect(programRequest(false, g, prog, f)).toMatchObject({ path: '/api/teams/platform/programs/forge-101', json: { base_sha: 'deadbeef' } })
    expect(enrollRequest(false, g, 'net-101')).toMatchObject({ method: 'PUT', path: '/api/teams/platform/programs/net-101' })
    expect(pinRequest(false, g, 'forge-101', '')).toMatchObject({ path: '/api/teams/platform/programs/forge-101/pin', json: { ref: '' } })
    expect(teamPath(false, 'platform')).toBe('/api/teams/platform')
  })
  test('the pages read from the endpoint the flag picks', () => {
    for (const inDB of [true, false]) {
      fetched.length = 0
      meFlag.inDB = inDB
      renderToStaticMarkup(<MemoryRouter><TeamPage /></MemoryRouter>)
      renderToStaticMarkup(<MemoryRouter><ProgramSettingsPage /></MemoryRouter>)
      expect(fetched).toEqual(Array(2).fill(inDB ? '/api/org/teams/platform' : '/api/teams/platform'))
    }
  })
  test('inline windows are kept unless a named schedule replaces them', () => {
    const inl = { ...prog, inline_schedule: { timezone: 'UTC' } }
    expect(programRequest(true, team, inl, { ...f, schedule: '' }).json).toHaveProperty('inline_schedule')
    expect(programRequest(true, team, inl, { ...f, schedule: 'nights' }).json).not.toHaveProperty('inline_schedule')
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
  test('team page renders from the org read, with a labelled save for each form', () => {
    const h = renderToStaticMarkup(<MemoryRouter><TeamPage /></MemoryRouter>)
    expect(h).toContain('Save roster')
    expect(h).toContain('Save budget')
  })
  test('program page renders and announces saves', () => {
    const h = renderToStaticMarkup(<MemoryRouter><ProgramSettingsPage /></MemoryRouter>)
    expect(h).toContain('Save program')
    expect(h).toContain('role="status"')
  })
  test('create-team has labelled id, name and leader', () => {
    const h = renderToStaticMarkup(<MemoryRouter><CreateTeam /></MemoryRouter>)
    for (const l of ['Team id', 'Name', "Leader email", 'Create team']) expect(h).toContain(l)
  })
})
