import type { UnbilledLab, UnbilledWindow, UsageInterval } from '../types'

const Q = 15 * 60_000 // marks snap and step by 15 minutes
const ms = (s?: string) => (s ? Date.parse(s) : undefined)

// cutFor: a lab's own mark, else the account mark, else the window's start; never before the window.
// `own` holds a lab's mark while it is being dragged, before the server's reply; `lab.own_mark` is the saved one.
export function cutFor(lab: UnbilledLab, accountMark: string | undefined, own: Record<string, number>, from: number): number {
  const c = own[lab.id] ?? ms(lab.own_mark) ?? ms(accountMark) ?? from
  return Math.max(c, from)
}

// split: how much of an interval lies left of the cut (already billed), and the dollars right of it.
export function split(iv: UsageInterval, cut: number): { billedFrac: number; usd: number } {
  const f = Date.parse(iv.from), t = Date.parse(iv.to)
  const b = Math.min(Math.max(cut, f), t)
  return { billedFrac: t > f ? (b - f) / (t - f) : 1, usd: iv.hourly_usd * (t - b) / 3_600_000 }
}

export function unbilledTotal(w: UnbilledWindow, accountMark: string | undefined, own: Record<string, number>) {
  let usd = 0, labs = 0
  for (const l of w.labs) {
    const cut = cutFor(l, accountMark, own, Date.parse(w.from))
    const u = l.intervals.reduce((s, iv) => s + split(iv, cut).usd, 0)
    if (u > 0.004) { usd += u; labs++ }
  }
  return { usd, labs }
}

export function summary(w: UnbilledWindow, accountMark: string | undefined, own: Record<string, number>): string {
  const { usd, labs } = unbilledTotal(w, accountMark, own)
  const sep = w.labs.filter((l) => own[l.id] !== undefined || l.own_mark).length
  return `Crucible adds $${usd.toFixed(2)} from ${labs} lab${labs === 1 ? '' : 's'} on top of the AWS bill` +
    (sep ? ` · ${sep} lab${sep === 1 ? '' : 's'} set on ${sep === 1 ? 'its' : 'their'} own` : '')
}

export const snap = (t: number, from: number, to: number) => Math.min(Math.max(from + Math.round((t - from) / Q) * Q, from), to)
export const step = (t: number, dir: -1 | 1, from: number, to: number) => Math.min(Math.max(t + dir * Q, from), to)
export const at = (t: number, from: number, to: number) => ((t - from) / (to - from)) * 100
// hhmm: a time on the timeline, in the browser's zone.
export const hhmm = (t: number) => new Date(t).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' })
