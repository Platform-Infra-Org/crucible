import { expect, test, vi } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { LedgerPage } from './Ledger'
import type { AccountBlock, Ledger, LedgerLab } from '../types'
import { hhmm } from '../lib/billedTimeline'

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

test('without a billing export the Ledger says why Billed stays empty, not that it is late', () => {
  ledger = { ...base, actuals_as_of: undefined, no_billing_export: true, prices_stale: false } as Ledger
  const h = page()
  expect(h).toContain('<p role="status" data-testid="actuals-status" class="muted">This install reads no AWS bill (no billing export), so Billed stays empty: spend comes from metered costs and estimates.</p>')
  expect(h).not.toContain('No AWS costs have been read yet.')
})

const account = (extra: Partial<AccountBlock> = {}): AccountBlock => ({
  billed_usd: 412.37, read_at: new Date().toISOString(), unbilled_usd: 4.13, reserved_usd: 38, limit_usd: 600, source: 'budget', stale: false,
  can_mark: false, window: { from: new Date(Date.now() - 24 * 3600e3).toISOString(), to: new Date().toISOString(), labs: [], unbilled_usd: 4.13 }, ...extra,
})

test('the AWS account line adds the bill, what is not yet billed and what running labs reserve, against the limit', () => {
  const a = account()
  ledger = { ...base, prices_stale: false, account: a } as Ledger
  let h = page()
  expect(h).toContain(`AWS account this month: $412.37 billed (as of ${hhmm(Date.parse(a.read_at!))}) + $4.13 not yet billed + $38.00 reserved by running labs = $454.50 of $600.00`)
  expect(h).toMatch(/<meter [^>]*aria-label="AWS account spend this month" min="0" max="600" low="480" high="600" optimum="0" value="454\.5/)
  expect(h).toContain('AWS updates the bill a few times a day; reserved is what running labs may still spend.')
  expect(h).toContain('Not yet on the AWS bill · last 24 hours')
  expect(h.indexOf('AWS account this month')).toBeLessThan(h.indexOf('<h2>Budgets</h2>'))
  expect(h).not.toContain('Out of date: new paid labs')
  ledger = { ...base, prices_stale: false, account: account({ limit_usd: 0 }) } as Ledger
  h = page()
  expect(h).toMatch(/reserved by running labs = \$454\.50<\/p>/)
  expect(h).not.toContain('AWS account spend this month')
})

test('a stale bill says paid labs need an admin, and admins see why', () => {
  ledger = { ...base, prices_stale: false, account: account({ stale: true }) } as Ledger
  let h = page()
  expect(h).toMatch(/<p role="status" data-testid="account-status" class="error">Out of date: new paid labs need an admin until the bill is read again\.<\/p>/)
  ledger = { ...base, prices_stale: false, account: account({ stale: true, error: 'no spend source: re-run labs-init, then up' }) } as Ledger
  h = page()
  expect(h).toContain('Out of date: new paid labs need an admin until the bill is read again. Last error: no spend source: re-run labs-init, then up')
})

test('a stale bill with no limit set only says it is out of date', () => {
  ledger = { ...base, prices_stale: false, account: account({ stale: true, limit_usd: 0 }) } as Ledger
  let h = page()
  expect(h).toMatch(/<p role="status" data-testid="account-status" class="error">Out of date\.<\/p>/)
  expect(h).not.toContain('need an admin')
  ledger = { ...base, prices_stale: false, account: account({ stale: true, limit_usd: 0, error: 'AccessDenied' }) } as Ledger
  h = page()
  expect(h).toContain('Out of date. Last error: AccessDenied')
})

test('the Refresh button says when the AWS bill can be read again', () => {
  ledger = { ...base, can_refresh: true, prices_stale: false, account: account({ refresh_after: new Date(Date.now() + 125_000).toISOString() }) } as Ledger
  expect(page()).toMatch(/The AWS bill can be read again in 2:0\d\./)
  ledger = { ...base, can_refresh: true, prices_stale: false, account: account({ refresh_after: new Date(Date.now() - 1000).toISOString() }) } as Ledger
  expect(page()).not.toContain('can be read again')
})

test('without the account block (no AWS labs, or not an approver) there is no account line', () => {
  ledger = { ...base, prices_stale: false } as Ledger
  const h = page()
  expect(h).not.toContain('AWS account this month')
  expect(h).not.toContain('Not yet on the AWS bill')
})
