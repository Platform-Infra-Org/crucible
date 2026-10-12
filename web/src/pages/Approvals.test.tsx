import { expect, test, vi } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router'
import type { Approval } from '../types'
import { ApprovalsPage } from './Approvals'

const spend = { budget_usd: 10, cap_usd: 10, spent_usd: 1, committed_usd: 0 }
const a = {
  id: 'x1', requester: 't@x', requester_name: 'Tara', requester_smith: { level: 3, title: 'Smith' }, team: 'forge', training: 'f101', module: 'm1',
  lab_title: 'Lab', runtime: 'aws', hourly_usd: 1, estimate_usd: 2, ttl_s: 3600, tier: 'leader', over_cap: false, requested_at: '2026-10-11T00:00:00Z',
  team_spend: spend, program_spend: spend, recent: [], schedule: { name: '', text: '', open: true }, kind: 'request',
} as unknown as Approval
vi.mock('../useFetch', () => ({ useFetch: () => ({ data: [a], error: undefined, reload: () => {} }) }))
vi.mock('../me', () => ({ useMe: () => ({ me: { is_admin: true, user: { email: 'a@x' } } }) }))

test('the requester carries their smith rank', () => {
  const h = renderToStaticMarkup(<MemoryRouter><ApprovalsPage /></MemoryRouter>)
  expect(h).toContain('Tara <span class="muted">t@x</span> <span class="smith-mark l3">')
  expect(h).not.toMatch(/rank #|\btop |leaderboard/i)
})
