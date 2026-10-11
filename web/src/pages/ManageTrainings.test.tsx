import { describe, expect, test, vi } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router'
import type { ProgramConfig, TrainingsAdmin } from '../types'
import { ManageTrainingsPage } from './ManageTrainings'

const prog = (over: Partial<ProgramConfig> = {}): ProgramConfig => ({
  team: 'forge', training: 'forge-101', title: 'Forge 101', version: 2,
  roles: { participants: ['all_trainees', 'guest@x'], editors: ['forge_seniors'], admins: [] },
  resolved: { participants: ['a@x', 'b@x', 'guest@x'], editors: ['s@x'], admins: [] },
  schedule: '', lab_defaults: { ttl: '', idle_timeout: '', max_extension: '' }, budget_usd_month: 0, review_self_reported: false,
  can_manage: true, running_sha: 'a'.repeat(40), head_sha: 'a'.repeat(40), pinned_ref: '', ...over,
})

let page: TrainingsAdmin
const me = { can_edit_content: false }
const other: Record<string, unknown> = {
  '/api/org/people': { people: [{ email: 'guest@x', name: '', team: '', tag: '' }, { email: 'a@x', name: 'Ann', team: 'forge', tag: 'trainee' }],
    groups: ['all', 'all_trainees', 'forge', 'forge_seniors'] },
  '/api/content': [{ id: 'forge-101', title: 'Forge 101' }],
  '/api/edits': [{ id: 7, training: 'forge-101', title: 'Sharper intro', author: 'l@x', status: 'pending' },
    { id: 8, training: 'forge-201', title: 'Elsewhere', author: 'l@x', status: 'pending' }],
  '/api/authoring/drafts': [{ id: 3, training: 'forge-101', title: 'Half done', state: 'draft' }],
}
vi.mock('../useFetch', () => ({ useFetch: (path: string) => ({ data: path === '/api/org/trainings' ? page : other[path], error: undefined, reload: () => {} }) }))
vi.mock('../me', () => ({ useMe: () => ({ me }) }))
vi.mock('react-router', async (orig) => ({ ...(await orig<typeof import('react-router')>()), useParams: () => ({ training: 'forge-101' }) }))
const render = () => renderToStaticMarkup(<MemoryRouter><ManageTrainingsPage /></MemoryRouter>)
const base = { schedules: ['nights'], teams: [], my_team: 'forge' }

describe('manage trainings', () => {
  test('a manager sees the source and gives roles to people and groups', () => {
    page = { ...base, is_admin: false, can_create: true, trainings: [
      { id: 'forge-101', title: 'Forge 101', available: true, team: 'forge', can_manage: true, repo: 'https://***@git.example/f.git', ref: 'main', program: prog() },
    ] }
    const h = render()
    expect(h).toContain('https://***@git.example/f.git')
    expect(h).toContain('<dt>Branch, tag or commit</dt><dd><code>main</code></dd><dt>Folder</dt><dd>the repository root</dd>')
    expect(h).toContain('forge-101 · forge · 3 taking it')
    expect(h).toContain('aria-label="Remove guest@x from participants"')
    expect(h).toContain('aria-label="Remove all_trainees from participants"')
    expect(h).toContain('title="the trainees of every team"') // a group says who it is
    expect(h).toContain('Now 3 people.')
    expect(h).toContain('<option value="forge_seniors">the seniors of forge</option>')
    expect(h).toContain('<option value="a@x">Ann · trainee on forge</option>')
    expect(h).toContain('+ New training')
    expect(h).toMatch(/<button type="button" class="ghost danger-text">Delete training<\/button>/)
    expect(h).not.toContain('Give to this team') // only admins move a training
  })
  test('an admin gives a training to another team', () => {
    page = { ...base, is_admin: true, can_create: true, teams: [{ id: 'forge', name: 'The Forge' }, { id: 'anvil', name: 'Anvil' }], trainings: [
      { id: 'forge-101', title: 'Forge 101', available: true, team: 'forge', can_manage: true, repo: 'r', ref: 'main', program: prog() },
    ] }
    const h = render()
    expect(h).toContain('Give to this team')
    expect(h).toMatch(/<option value="anvil"[^>]*>Anvil<\/option>/)
  })
  test('an editor reads the roles without controls', () => {
    page = { ...base, is_admin: false, can_create: false, trainings: [
      { id: 'forge-101', title: 'Forge 101', available: true, team: 'forge', can_manage: false, program: prog({ can_manage: false }) },
    ] }
    const h = render()
    expect(h).toContain('Read-only')
    expect(h).toContain('guest@x')
    expect(h).not.toContain('Remove guest@x')
    expect(h).not.toContain('Repository')
    expect(h).not.toContain('Delete training')
    expect(h).not.toContain('New training')
    expect(h).not.toContain('Lab settings')
    expect(h).not.toContain('Content edits') // not an author
  })
  test("an author edits the training's content from here and sees its drafts and edits, not other trainings'", () => {
    page = { ...base, is_admin: false, can_create: false, trainings: [
      { id: 'forge-101', title: 'Forge 101', available: true, team: 'forge', can_manage: false, program: prog({ can_manage: false }) },
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
