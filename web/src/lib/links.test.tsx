/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { expect, test, vi } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router'
import { LabsPage } from '../pages/Labs'
import { TrainingsPage } from '../pages/Trainings'
import { TrainingPage } from '../pages/Training'
import { AnvilDetailPage } from '../pages/Anvil'
import { ErrorBox } from '../components/ErrorBox'
import { ApiError } from '../api'

const outline = { title: 'T', description: '', percent: 0, modules: [] }
const sub = { id: 4, trainee: 't@x', trainee_name: 'T', trainee_smith: { level: 1, title: 'S' }, team: 'forge', training: 'f', module: 'm', kind: 'question', item: 'q', type: 'text', prompt: 'Why?', rubric: '', max_points: 5, answer: 'a', files: [], status: 'pending', points: 0, feedback: '', created_at: '2026-10-11T00:00:00Z' }
const data = (path: string) =>
  path === '/api/me/forge' ? { swords: [] } : path === '/api/labs' ? [{ id: 1, title: 'L', training: 't', state: 'ready', created_at: '2026-10-11T00:00:00Z', ends_at: '', link: '/p/forge/f/m/m/lab' }]
    : path.startsWith('/api/programs/') ? outline
      : path.startsWith('/api/anvil/4') ? { submission: sub, history: [] }
        : [{ id: 'f', title: 'F', description: '', estimated_hours: 1, modules: 1, enrolled: [{ id: 'forge', name: 'Forge' }], available: true }]
vi.mock('../useFetch', () => ({ useFetch: (p: string) => ({ data: data(p), error: undefined, reload: () => {} }) }))
vi.mock('../me', () => ({ useMe: () => ({ me: { is_admin: true, teams: ['forge'], user: { email: 'a@x' } } }) }))
vi.mock('react-router', async (orig) => ({ ...(await orig<typeof import('react-router')>()), useParams: () => ({ id: '4', team: 'forge', training: 'f' }) }))
const render = (el: React.ReactElement) => renderToStaticMarkup(<MemoryRouter>{el}</MemoryRouter>)

test('back links are action links with the arrow in CSS, not in the text', () => {
  for (const h of [render(<TrainingPage />), render(<AnvilDetailPage />), render(<ErrorBox error={new ApiError(500, 'x')} />)]) {
    expect(h).toMatch(/<a class="action back"[^>]*>[^←<]*<\/a>/)
    expect(h).not.toContain('←')
  }
})

test('Open links are action links', () => {
  expect(render(<LabsPage />)).toMatch(/<a class="action" href="\/p\/forge\/f\/m\/m\/lab"[^>]*>Open<\/a>/)
  expect(render(<TrainingsPage />)).toMatch(/<a class="action" href="\/p\/forge\/f"[^>]*>Open \(Forge\)<\/a>/)
})

test('the other standalone links carry the class in source', () => {
  const src = (f: string) => readFileSync(new URL(`../pages/${f}`, import.meta.url), 'utf8')
  expect(src('Team.tsx')).toMatch(/<Link className="action" to="\/trainings\/manage\?new">Register a training/)
  expect(src('Lab.tsx')).toMatch(/<Link className="action back" to=\{back\}>Back to the training/)
  expect(src('Lab.tsx')).toMatch(/<Link className="action" to="\/connect">Connect your laptop/)
  expect(src('Lab.tsx')).toMatch(/<Link className="action back" to=\{back\} aria-label="Back to the training"><\/Link>/)
  expect(src('Reading.tsx')).toMatch(/className="action back"/)
  expect(src('Quiz.tsx').match(/className="action back"/g)).toHaveLength(2)
  for (const f of ['Lab.tsx', 'Reading.tsx', 'Quiz.tsx', 'Anvil.tsx', 'Training.tsx']) expect(src(f)).not.toContain('>← ')
})

test('a bare link is accent with a quieter underline (colour alone must not mark it)', () => {
  const css = readFileSync(new URL('../theme/app.css', import.meta.url), 'utf8')
  const base = css.match(/^a \{[^}]*\}/m)![0]
  expect(base).toContain('color: var(--accent)')
  expect(base).toMatch(/text-decoration-color:\s*color-mix/)
  expect(css).toContain('a.action')
})
