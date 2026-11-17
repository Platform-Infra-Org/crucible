import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { cutFor, markOf, split, unbilledTotal, snap, step, at, summary, markGestures, type Drag, type MarkCtx } from './billedTimeline'
import type { UnbilledWindow } from '../types'

const H = 3600_000
const to = Date.parse('2026-11-14T14:00:00Z'), from = to - 24 * H
const iso = (ms: number) => new Date(ms).toISOString()
const win = (extra: Partial<UnbilledWindow> = {}): UnbilledWindow => ({ from: iso(from), to: iso(to), read_at: iso(to), unbilled_usd: 0, labs: [
  { id: 'a', team: 't', training: 'forge-401', module: 'm', requester: 'dana@x', hourly_usd: 1, unbilled_usd: 0,
    intervals: [{ from: iso(from + 1 * H), to: iso(from + 5 * H), hourly_usd: 1 }] },
  { id: 'b', team: 't', training: 'forge-402', module: 'm', requester: 'omer@x', hourly_usd: 2, unbilled_usd: 0,
    intervals: [{ from: iso(to - 2 * H), to: iso(to), hourly_usd: 2 }] },
], ...extra })

test('a lab\'s marker is its own mark, else the account mark, else the window start', () => {
  const w = win({ account_mark: iso(from + 3 * H) })
  expect(markOf(w.labs[0], w.account_mark, {}, from)).toBe(from + 3 * H)
  expect(markOf(w.labs[0], w.account_mark, { a: from + 4 * H }, from)).toBe(from + 4 * H)
  expect(markOf(w.labs[0], undefined, {}, from)).toBe(from)
  expect(markOf(w.labs[0], iso(from - 5 * H), {}, from)).toBe(from) // clamped
  expect(markOf({ ...w.labs[0], own_mark: iso(from + 2 * H) }, w.account_mark, {}, from)).toBe(from + 2 * H) // the saved own mark
})

test('the cut is the marker, never later than the last read of the bill; before any read no mark counts', () => {
  const w = win({ account_mark: iso(from + 3 * H) })
  expect(cutFor(w.labs[0], w.account_mark, {}, from, iso(to))).toBe(from + 3 * H)
  expect(cutFor(w.labs[0], w.account_mark, { a: from + 4 * H }, from, iso(from + 2 * H))).toBe(from + 2 * H)
  expect(cutFor(w.labs[0], w.account_mark, {}, from, undefined)).toBe(from)
  expect(unbilledTotal({ ...w, read_at: undefined }, w.account_mark, {})).toEqual({ usd: 4 + 4, labs: 2 })
})

test('ArrowUp steps like ArrowRight and ArrowDown like ArrowLeft', () => {
  let drag: Drag | null = null
  const c: MarkCtx = { from, to, width: () => 240, get: () => drag, set: (d) => { drag = d }, save: () => {} }
  const g = markGestures(), m = from + 19 * H
  expect(g.key(c, '', m, 'ArrowUp')).toBe(true)
  expect(drag).toEqual({ key: '', t: m + 15 * 60_000, orig: m })
  expect(g.key(c, '', m, 'ArrowDown')).toBe(true)
  expect(g.key(c, '', m, 'ArrowDown')).toBe(true)
  expect(drag).toEqual({ key: '', t: m - 15 * 60_000, orig: m })
  g.dispose()
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

test('marks snap to clock quarter-hours, not to the window start, so a saved mark stays put on every read', () => {
  const f = Date.parse('2026-11-13T14:07:23Z'), t = f + 24 * H // the window starts off the quarter-hour
  expect(snap(Date.parse('2026-11-14T09:07:00Z'), f, t)).toBe(Date.parse('2026-11-14T09:00:00Z'))
  expect(snap(Date.parse('2026-11-14T09:08:00Z'), f, t)).toBe(Date.parse('2026-11-14T09:15:00Z'))
  const saved = Date.parse('2026-11-14T09:30:00Z')
  expect(snap(saved, f, t)).toBe(saved)
  expect(snap(f, f, t)).toBe(f) // 14:07:23 rounds to 14:00, before the window: it clamps to the start
  expect(step(saved, 1, f, t)).toBe(Date.parse('2026-11-14T09:45:00Z'))
  expect(step(saved + 5 * 60_000, -1, f, t)).toBe(saved) // off-grid: to the previous quarter-hour
  expect(step(saved + 5 * 60_000, 1, f, t)).toBe(Date.parse('2026-11-14T09:45:00Z'))
  expect(step(f, -1, f, t)).toBe(f)
  expect(step(t - 60_000, 1, f, t)).toBe(t)
})

describe('marker gestures', () => {
  beforeEach(() => { vi.useFakeTimers() })
  afterEach(() => { vi.useRealTimers() })
  const setup = () => {
    let drag: Drag | null = null
    const saved: Drag[] = []
    const c: MarkCtx = { from, to, width: () => 240, get: () => drag, set: (d) => { drag = d }, save: (d) => { saved.push(d) } }
    return { c, saved, g: markGestures(), drag: () => drag }
  }
  const m = from + 19 * H

  test('a click on a marker that does not move it saves nothing', () => {
    const { c, saved, g, drag } = setup()
    g.down(c, '', m, 100) // 100 px is hours from the mark on a 240 px lane: the pointer's place does not count
    g.up(c, '')
    expect(saved).toEqual([])
    expect(drag()).toBeNull()
    g.down(c, '', m, 100)
    g.move(c, '', 100.2) // a tremble under a quarter-hour
    g.up(c, '')
    expect(saved).toEqual([])
  })

  test('a drag moves by the pointer travel and saves once on release', () => {
    const { c, saved, g } = setup()
    g.down(c, '', m, 100)
    g.move(c, '', 110) // 10 px of 240 = 1 h
    g.up(c, '')
    expect(saved).toEqual([{ key: '', t: m + H, orig: m }])
  })

  test('keys save 400 ms after the last press; another slider flushes the first one; nothing fires after dispose', () => {
    const { c, saved, g } = setup()
    expect(g.key(c, '', m, 'ArrowRight')).toBe(true)
    g.keyUp(c, 'ArrowRight')
    g.key(c, 'b', to - H, 'ArrowLeft') // within 400 ms: the account marker's move is saved first
    expect(saved).toEqual([{ key: '', t: m + 15 * 60_000, orig: m }])
    g.keyUp(c, 'ArrowLeft')
    vi.advanceTimersByTime(400)
    expect(saved).toEqual([{ key: '', t: m + 15 * 60_000, orig: m }, { key: 'b', t: to - H - 15 * 60_000, orig: to - H }])
    g.key(c, '', m, 'Home') // b's save is in flight (its drag still shows): the other slider must not send it again
    expect(saved).toHaveLength(2)
    g.keyUp(c, 'Home')
    g.dispose() // the Ledger went away: the pending save never fires
    vi.advanceTimersByTime(1000)
    expect(saved).toHaveLength(2)
    expect(g.key(c, '', m, 'a')).toBe(false)
  })
})
