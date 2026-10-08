import { describe, expect, test, vi } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router'
import type { TeamProgram, TrainingsAdmin } from '../types'
import { ManageTrainingsPage } from './ManageTrainings'

const prog = (over: Partial<TeamProgram> = {}): TeamProgram => ({
  team: 'forge', training: 'forge-101', title: 'Forge 101', version: 2, enrolled: ['a@x'],
  roles: { manager: [], scorers: ['s@x'], approvers: [] }, effective_roles: { manager: ['l@x'], scorers: ['s@x'], approvers: ['l@x'] },
  schedule: '', lab_defaults: { ttl: '', idle_timeout: '', max_extension: '' }, budget_usd_month: 0, review_self_reported: false,
  can_manage: true, running_sha: 'a'.repeat(40), head_sha: 'a'.repeat(40), pinned_ref: '', ...over,
})
const roster = (id: string, edit: boolean) => ({ id, name: id === 'forge' ? 'The Forge' : 'Anvil', version: 1, leader: 'l@x', seniors: ['s@x'],
  members: [], trainees: ['a@x', 'b@x'], mentors: {}, can_edit_team: edit })

let page: TrainingsAdmin
const me = { can_edit_content: false }
const other: Record<string, unknown> = {
  '/api/content': [{ id: 'forge-101', title: 'Forge 101' }],
  '/api/edits': [{ id: 7, training: 'forge-101', title: 'Sharper intro', author: 'l@x', status: 'pending' },
    { id: 8, training: 'forge-201', title: 'Elsewhere', author: 'l@x', status: 'pending' }],
  '/api/authoring/drafts': [{ id: 3, training: 'forge-101', title: 'Half done', state: 'draft' }],
}
vi.mock('../useFetch', () => ({ useFetch: (path: string) => ({ data: path === '/api/org/trainings' ? page : other[path], error: undefined, reload: () => {} }) }))
vi.mock('../me', () => ({ useMe: () => ({ me }) }))
vi.mock('react-router', async (orig) => ({ ...(await orig<typeof import('react-router')>()), useParams: () => ({ training: 'forge-101' }) }))
const render = () => renderToStaticMarkup(<MemoryRouter><ManageTrainingsPage /></MemoryRouter>)

describe('manage trainings', () => {
  test('an admin sees the source, every team, who is enrolled and who holds each role', () => {
    page = { is_admin: true, schedules: ['nights'], teams: [roster('forge', true), roster('anvil', true)], trainings: [
      { id: 'forge-101', title: 'Forge 101', available: true, repo: 'https://***@git.example/f.git', branch: 'main', programs: [prog()] },
      { id: 'forge-201', title: 'Forge 201', available: false, repo: 'https://git.example/g.git', branch: 'main', programs: [] },
    ] }
    const h = render()
    expect(h).toContain('https://***@git.example/f.git')
    expect(h).toContain('forge-101 · 1 team · 1 enrolled')
    expect(h).toContain('aria-label="Remove a@x from enrolled"')
    expect(h).toContain('Default: l@x') // managers and approvers are unset: the leader by default
    expect(h).toContain('aria-label="Remove s@x from scorers"') // scorers are named explicitly
    expect(h).toContain('Start for this team') // anvil does not run it yet
    expect(h).toContain('+ New training')
    expect(h).toMatch(/<button type="button" class="ghost danger-text">Delete training<\/button>/) // enabled while teams run it
    expect(h).toContain('id="team-forge"') // the team page links here
    expect(h).not.toContain('pinned to content') // no repoint warning until a new repo is typed
  })
  test('someone who cannot manage the program reads it without controls', () => {
    page = { is_admin: false, schedules: [], teams: [roster('forge', false)], trainings: [
      { id: 'forge-101', title: 'Forge 101', available: true, programs: [prog({ can_manage: false })] },
    ] }
    const h = render()
    expect(h).toContain('Read-only')
    expect(h).toContain('a@x')
    expect(h).not.toContain('Remove a@x')
    expect(h).not.toContain('Repository')
    expect(h).not.toContain('Delete training')
    expect(h).not.toContain('New training')
    expect(h).not.toContain('Start for this team')
    expect(h).not.toContain('Lab settings')
    expect(h).not.toContain('Content edits') // not an author
  })
  test("an author edits the training's content from here and sees its drafts and edits, not other trainings'", () => {
    page = { is_admin: false, schedules: [], teams: [roster('forge', false)], trainings: [
      { id: 'forge-101', title: 'Forge 101', available: true, programs: [prog({ can_manage: false })] },
    ] }
    me.can_edit_content = true
    const h = render()
    me.can_edit_content = false
    expect(h).toContain('aria-label="Content edits"')
    expect(h).toContain('href="/edits/new?training=forge-101"')
    expect(h).toContain('href="/edits/drafts/3"')
    expect(h).toContain('href="/edits/7"')
    expect(h).not.toContain('Elsewhere')
  })
})
