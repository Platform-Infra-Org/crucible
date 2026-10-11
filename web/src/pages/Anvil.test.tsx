import { expect, test, vi } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router'
import type { AnvilDetail, SignOff, Submission } from '../types'
import { AnvilDetailPage, AnvilPage } from './Anvil'

const smith = { level: 3, title: 'Smith' }
const sub: Submission = {
  id: 4, trainee: 't@x', trainee_name: 'Tara', trainee_smith: smith, team: 'forge', training: 'f101', module: 'm1', kind: 'question', item: 'q1',
  type: 'text', prompt: 'Why?', rubric: '', max_points: 5, answer: 'Because', files: [], status: 'pending', points: 0, feedback: '', created_at: '2026-10-11T00:00:00Z',
}
const off: SignOff = { team: 'forge', training: 'f101', module: 'm1', question: 'q2', prompt: 'Show me', points: 5, trainee: 'u@x', trainee_name: 'Uma', trainee_smith: smith }
const detail: AnvilDetail = { submission: sub, history: [] }
vi.mock('../useFetch', () => ({
  useFetch: (path: string) => ({ data: path.startsWith('/api/anvil/signoffs') ? [off] : path.startsWith('/api/anvil/4') ? detail : [sub], error: undefined, reload: () => {} }),
}))
vi.mock('../me', () => ({ useMe: () => ({ me: { is_admin: true, user: { email: 'a@x' } } }) }))
vi.mock('react-router', async (orig) => ({ ...(await orig<typeof import('react-router')>()), useParams: () => ({ id: '4' }) }))
const render = (el: React.ReactElement) => renderToStaticMarkup(<MemoryRouter>{el}</MemoryRouter>)

test('the queue and the sign-offs name the trainee with their smith rank', () => {
  const h = render(<AnvilPage />)
  expect(h.match(/smith-mark l3/g)).toHaveLength(2)
  expect(h).not.toMatch(/rank #|\btop |leaderboard/i)
})

test('the detail header does too, and a reset never lowers ranks', () => {
  const h = render(<AnvilDetailPage />)
  expect(h).toContain('smith-mark l3')
  expect(h).toContain('Sword and smith ranks are never lowered by a reset.')
  expect(h).not.toMatch(/rank #|\btop |leaderboard/i)
})
