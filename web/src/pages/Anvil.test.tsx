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
let detail: AnvilDetail = { submission: sub, history: [] }
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

// A pending upload with a rubric, a link answer, two files and one earlier attempt that was returned.
const pendingQuestion: Submission = {
  ...sub, type: 'upload', rubric: 'Hard but brittle.', answer: 'https://example.com/log', files: [{ name: 'forge.png', size: 2048 }, { name: 'forge.log', size: 10 }],
}
const earlier: Submission = { ...sub, id: 3, status: 'returned', scored_by: 's@x', feedback: 'Attach the log itself.' }
const renderScoring = (s: Submission) => {
  detail = { submission: s, history: [earlier] }
  return render(<AnvilDetailPage />)
}

test('lays the scoring view out as panels with the decision beside the work', () => {
  const html = renderScoring(pendingQuestion)
  expect(html).toContain('class="scoring-grid"')
  for (const h of ['Prompt', 'Answer key', 'Answer', 'Your score', 'Earlier attempts']) expect(html).toMatch(new RegExp(`>${h}</h2>`))
  expect(html).toContain('Only scorers see this')
  expect(html).toMatch(/<form class="stack panel decision"/)
  expect(html.indexOf('Your score')).toBeLessThan(html.indexOf('Earlier attempts')) // decision right after the answer
  expect(html.indexOf('>Answer</h2>')).toBeLessThan(html.indexOf('Your score'))
  expect(html).toContain('class="chips attachments"')
  expect(html).toMatch(/<a class="chip" href="\/api\/submissions\/4\/files\/1"[^>]*>forge.log/)
  expect(html).toMatch(/<a class="action" href="https:\/\/example.com\/log" target="_blank" rel="noopener noreferrer" title="[^"]*new tab[^"]*"/)
  expect(html).toContain('class="action back"')
  expect(html).toMatch(/<ol class="attempts">.*<span class="badge warn">Returned<\/span>/)
  expect(html).toMatch(/<button[^>]*type="submit"[^>]*class="primary"[^>]*>Score<\/button>|<button[^>]*class="primary"[^>]*type="submit"[^>]*>Score<\/button>/)
  expect(html).toMatch(/<button type="button" class="ghost"[^>]*>Return for rework<\/button>/)
})

test('shows a decided submission read-only', () => {
  const html = renderScoring({ ...pendingQuestion, status: 'scored', scored_by: 'senior@example.com', points: 4, feedback: 'Good work.' })
  expect(html).not.toContain('<textarea')
  expect(html).toMatch(/>Scored<\/h2>/)
  expect(html).toContain('senior@example.com')
  expect(html).toContain('Good work.')
  expect(html).toMatch(/4<\/strong> of 5/)
})
