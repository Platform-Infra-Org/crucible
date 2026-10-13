import { expect, test, vi } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { LedgerPage } from './Ledger'
import type { Ledger, LedgerLab } from '../types'

const lab = (id: string, extra: Partial<LedgerLab>): LedgerLab => ({
  id, team: 'forge', training: 'forge-401', module: 'cloud', requester: 't@x', runtime: 'aws', state: 'destroyed',
  hourly_usd: 0.5, estimate_usd: 2, cost_usd: 0.75, settled: false, created_at: '2026-10-10T10:00:00Z', ...extra,
})
let ledger: Ledger
// React separates adjacent text with <!-- -->; the reader sees one sentence
const page = () => renderToStaticMarkup(<LedgerPage />).replaceAll('<!-- -->', '')
vi.mock('../useFetch', () => ({ useFetch: () => ({ data: ledger, error: undefined, reload: () => {} }) }))

const base = {
  month: '2026-10', aws: true, actuals_as_of: '2026-10-11T10:00:00Z', actuals_stale: false, reaper_stale: false, can_refresh: false,
  teams: [], daily: [], running: [], top_spenders: [], accuracy: { labs: 0, estimate_usd: 0, actual_usd: 0 },
  labs: [lab('l1', { metered_usd: 0.75, actual_usd: 0.6, settled: true }), lab('l2', {})],
}

test('each lab shows its estimate, metered and billed spend', () => {
  ledger = { ...base, prices_as_of: '2026-10-09T03:00:00Z', prices_stale: false } as Ledger
  const h = page()
  expect(h).toMatch(/<th>Estimate<\/th>.*<th>Metered<\/th><th>Billed<\/th>/)
  expect(h).not.toContain('Actual')
  expect(h).toContain('<td>$0.75</td><td>$0.60</td></tr>') // l1: metered, then billed
  expect(h).toContain('<td>—</td><td>—</td></tr>')          // l2: neither yet
  expect(h).toMatch(/<p role="status" data-testid="prices-status" class="muted">Price data from [^<]+\.<\/p>/)
})

test('stale price data is said in words, in the danger colour, with the reason when the server gives it (admins)', () => {
  ledger = { ...base, prices_as_of: '2026-09-01T03:00:00Z', prices_stale: true } as Ledger
  let h = page()
  expect(h).toMatch(/data-testid="prices-status" class="error">Price data from [^<]+\. Out of date: AWS labs can(&#x27;|')t be estimated or requested until it refreshes\.<\/p>/)
  ledger = { ...ledger, prices_error: 'pricing.us-east-1.amazonaws.com: 503' }
  h = page()
  expect(h).toContain('Last error: pricing.us-east-1.amazonaws.com: 503')
  ledger = { ...base, prices_stale: true } as Ledger
  expect(page()).toContain('No price data yet.')
})
