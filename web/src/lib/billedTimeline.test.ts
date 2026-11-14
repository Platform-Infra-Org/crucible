import { expect, test } from 'vitest'
import { cutFor, split, unbilledTotal, snap, step, at, summary } from './billedTimeline'
import type { UnbilledWindow } from '../types'

const H = 3600_000
const to = Date.parse('2026-11-14T14:00:00Z'), from = to - 24 * H
const iso = (ms: number) => new Date(ms).toISOString()
const win = (extra: Partial<UnbilledWindow> = {}): UnbilledWindow => ({ from: iso(from), to: iso(to), unbilled_usd: 0, labs: [
  { id: 'a', team: 't', training: 'forge-401', module: 'm', requester: 'dana@x', hourly_usd: 1, unbilled_usd: 0,
    intervals: [{ from: iso(from + 1 * H), to: iso(from + 5 * H), hourly_usd: 1 }] },
  { id: 'b', team: 't', training: 'forge-402', module: 'm', requester: 'omer@x', hourly_usd: 2, unbilled_usd: 0,
    intervals: [{ from: iso(to - 2 * H), to: iso(to), hourly_usd: 2 }] },
], ...extra })

test('a lab is cut at its own mark, else the account mark, else the window start', () => {
  const w = win({ account_mark: iso(from + 3 * H) })
  expect(cutFor(w.labs[0], w.account_mark, {}, from)).toBe(from + 3 * H)
  expect(cutFor(w.labs[0], w.account_mark, { a: from + 4 * H }, from)).toBe(from + 4 * H)
  expect(cutFor(w.labs[0], undefined, {}, from)).toBe(from)
  expect(cutFor(w.labs[0], iso(from - 5 * H), {}, from)).toBe(from) // clamped
  expect(cutFor({ ...w.labs[0], own_mark: iso(from + 2 * H) }, w.account_mark, {}, from)).toBe(from + 2 * H) // the saved own mark
})

test('an interval splits into its billed and counted parts at the cut', () => {
  expect(split({ from: iso(from + 1 * H), to: iso(from + 5 * H), hourly_usd: 1 }, from + 2 * H)).toEqual({ billedFrac: 0.25, usd: 3 })
  expect(split({ from: iso(from + 1 * H), to: iso(from + 5 * H), hourly_usd: 1 }, from + 9 * H)).toEqual({ billedFrac: 1, usd: 0 })
})

test('the total and the summary follow the marks', () => {
  const w = win({ account_mark: iso(from + 3 * H) })
  expect(unbilledTotal(w, w.account_mark, {})).toEqual({ usd: 2 + 4, labs: 2 })
  expect(summary(w, w.account_mark, { b: to - 1 * H })).toBe('Crucible adds $4.00 from 2 labs on top of the AWS bill · 1 lab set on its own')
  expect(summary(w, iso(to), {})).toBe('Crucible adds $0.00 from 0 labs on top of the AWS bill')
})

test('marks snap to 15 minutes, step by 15 with the arrow keys, and stay in the window', () => {
  expect(snap(from + 7 * 60_000, from, to)).toBe(from + 0)
  expect(snap(from + 8 * 60_000, from, to)).toBe(from + 15 * 60_000)
  expect(step(from, -1, from, to)).toBe(from)
  expect(step(to - 10 * 60_000, 1, from, to)).toBe(to)
  expect(at(from + 12 * H, from, to)).toBe(50) // percent across
})
