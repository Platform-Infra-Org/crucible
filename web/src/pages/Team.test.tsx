import { describe, expect, test, vi } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router'
import { ApiError } from '../api'
import { Conflict, reportSaveError } from '../components/Conflict'
import { budgetRequest, programRequest, rosterRequest } from '../lib/teamRequests'
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
vi.mock('../useFetch', () => ({ useFetch: () => ({ data: team, error: undefined, reload: () => {} }) }))

describe('saves echo the version the page read', () => {
  test('roster, budget and program', () => {
    const r = rosterRequest(team, { seniors: '', members: '', trainees: 'a@x', mentors: '' })
    expect(r.path).toBe('/api/org/teams/platform/roster')
    expect(r.json).toMatchObject({ version: 3, name: 'Platform', leader: 'l@x', trainees: ['a@x'] })
    expect(budgetRequest(team, '10', '').json).toEqual({ version: 5, monthly_usd: 10, hard_cap_usd: 0 })
    expect(budgetRequest({ ...team, budget: undefined }, '4', '').json.version).toBe(0) // no budget row yet
    const p = programRequest('platform', prog, { enrolled: new Set(['a@x']), managers: '', scorers: '', approvers: '', schedule: '', ttl: '', idle: '', ext: '', budget: '', reviewSelf: false })
    expect(p.path).toBe('/api/org/teams/platform/programs/forge-101')
    expect(p.json).toMatchObject({ version: 7, enrolled: ['a@x'] })
    expect(JSON.stringify(p.json)).not.toContain('base_sha')
  })
  test('inline windows are kept unless a named schedule replaces them', () => {
    const inl = { ...prog, inline_schedule: { timezone: 'UTC' } }
    const f = { enrolled: [], managers: '', scorers: '', approvers: '', ttl: '', idle: '', ext: '', budget: '', reviewSelf: false }
    expect(programRequest('t', inl, { ...f, schedule: '' }).json).toHaveProperty('inline_schedule')
    expect(programRequest('t', inl, { ...f, schedule: 'nights' }).json).not.toHaveProperty('inline_schedule')
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
