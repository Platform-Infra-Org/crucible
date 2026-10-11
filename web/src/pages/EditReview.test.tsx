import { describe, expect, test, vi } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router'
import type { ContentEdit } from '../types'
import { EditReviewPage } from './EditReview'

let edit: ContentEdit
vi.mock('react-router', async (orig) => ({ ...(await orig<typeof import('react-router')>()), useParams: () => ({ id: '13' }) }))
vi.mock('../useFetch', () => ({ useFetch: (path: string | null) => ({ data: path === '/api/edits/13' ? edit : undefined, error: undefined, reload: () => {} }) }))
vi.mock('../me', () => ({ useMe: () => ({ me: { can_edit_content: false, user: { email: 'senior@x' } } }) }))

const base: ContentEdit = {
  id: 13, training: 'forge-101', title: 'Fix a typo', author: 'admin@x', base_sha: 'a', branch: 'b', status: 'pending',
  created_at: '2026-10-11T00:00:00Z', diff: '', ops: [], can_review: false, can_withdraw: false,
}
const render = () => renderToStaticMarkup(<MemoryRouter><EditReviewPage /></MemoryRouter>)

describe('EditReviewPage', () => {
  test('a reviewer gets the review panel: a note, approve as the primary action, reject beside it', () => {
    edit = { ...base, can_review: true }
    const h = render()
    expect(h).toContain('Your review')
    expect(h).toMatch(/<label>Review note <textarea/)
    expect(h).toMatch(/<button class="primary">Approve and merge<\/button><button type="button" class="ghost">Reject<\/button>/)
    expect(h).not.toContain('Withdraw')
  })

  test('the author waits for review and can only withdraw', () => {
    edit = { ...base, can_withdraw: true }
    const h = render()
    expect(h).toContain('Waiting for review')
    expect(h).toContain('Withdraw')
    expect(h).not.toContain('Approve and merge')
    expect(h).not.toContain('textarea')
  })

  test('a decided edit shows its note with the reviewer and no actions', () => {
    edit = { ...base, status: 'rejected', reviewer: 'senior@x', note: 'Needs a lab.' }
    const h = render()
    expect(h).toContain('Review note from senior@x:</span> Needs a lab.')
    expect(h).not.toContain('<form')
  })
})
