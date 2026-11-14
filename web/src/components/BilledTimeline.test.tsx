import { expect, test } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { BilledTimeline } from './BilledTimeline'
import { hhmm } from '../lib/billedTimeline'
import type { UnbilledWindow } from '../types'

const H = 3600_000
const to = Date.parse('2026-11-14T14:00:00Z'), from = to - 24 * H
const iso = (ms: number) => new Date(ms).toISOString()
const mark = from + 19 * H // 09:00 UTC
const win = (extra: Partial<UnbilledWindow> = {}): UnbilledWindow => ({ from: iso(from), to: iso(to), unbilled_usd: 0, account_mark: iso(mark), labs: [
  { id: 'a', team: 't', training: 'forge-401', module: 'm', requester: 'dana@x', hourly_usd: 0.24, unbilled_usd: 0,
    intervals: [{ from: iso(from + 1 * H), to: iso(from + 5 * H), hourly_usd: 0.24 }] },
  { id: 'b', team: 't', training: 'forge-402', module: 'm', requester: 'omer@x', hourly_usd: 2, unbilled_usd: 0,
    intervals: [{ from: iso(to - 2 * H), to: iso(to), hourly_usd: 2 }] },
], ...extra })
// React separates adjacent text with <!-- -->; the reader sees one sentence
const html = (w: UnbilledWindow, canMark: boolean) => renderToStaticMarkup(<BilledTimeline w={w} canMark={canMark} onSaved={() => {}} />).replaceAll('<!-- -->', '')

test('an admin drags the account marker, sets a lab apart, or counts all 24 hours', () => {
  const h = html(win(), true)
  expect(h).toContain('<h5')
  expect(h).toContain('Not yet on the AWS bill · last 24 hours')
  expect(h).toMatch(/role="slider"[^>]*aria-label="Account: AWS has billed usage up to"/)
  expect(h).toContain(`aria-valuemin="${from}" aria-valuemax="${to}" aria-valuenow="${mark}" aria-valuetext="${hhmm(mark)}"`)
  expect(h).toContain(`Account billed up to ${hhmm(mark)} <span aria-hidden="true">⇆</span>`) // the arrow is decoration
  expect(h.match(/>Set separately</g)).toHaveLength(2)
  expect(h).toContain('>Count all 24 h<')
  expect(h).toMatch(/<p role="status"[^>]*>Crucible adds \$4\.00 from 1 lab on top of the AWS bill<\/p>/)
  expect(h).toContain('Check the month-to-date cost in the AWS console, then drag the green marker to the time it covers.')
  expect(h).toContain('forge-401')
  expect(h).toContain('dana@x')
  expect(h).toContain('$0.24/h')
  expect(h).toContain('>now<')
  expect(h).not.toContain('forge asleep')
})

test('a lab set on its own has a ◆, its own slider, and Follow account', () => {
  const w = win()
  w.labs[1].own_mark = iso(to - 1 * H)
  const h = html(w, true)
  expect(h).toContain('◆')
  expect(h).toMatch(/role="slider"[^>]*aria-label="Billed up to, forge-402 \(omer@x\), set on its own"/)
  expect(h).toContain('>Follow account<')
  expect(h.match(/>Set separately</g)).toHaveLength(1)
  expect(h).toContain('Crucible adds $2.00 from 1 lab on top of the AWS bill · 1 lab set on its own')
})

test('an approver sees the bars and the summary, nothing to move', () => {
  const h = html(win(), false)
  expect(h).toContain('Crucible adds $4.00 from 1 lab on top of the AWS bill')
  expect(h).toContain('class="billed"')
  expect(h).toContain('class="hot"')
  expect(h).not.toContain('role="slider"')
  expect(h).not.toContain('<button')
})

test('the night the forge slept is a band in words', () => {
  const h = html(win({ slept_from: iso(from + 6 * H), slept_to: iso(from + 17 * H) }), true)
  expect(h).toContain('forge asleep')
})

test('the legend says what each mark means in words', () => {
  const h = html(win(), true)
  for (const s of ['In the AWS bill', 'Counted by Crucible', 'Account marker', '◆ This lab set on its own']) expect(h).toContain(s)
})
