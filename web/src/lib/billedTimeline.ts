import type { UnbilledLab, UnbilledWindow, UsageInterval } from '../types'

const Q = 15 * 60_000 // marks snap and step by 15 minutes
const ms = (s?: string) => (s ? Date.parse(s) : undefined)

// markOf: where a lab's marker sits: its own mark, else the account mark, else the window's start; never before the window.
// `own` holds a lab's mark while it is being dragged, before the server's reply; `lab.own_mark` is the saved one.
export function markOf(lab: UnbilledLab, accountMark: string | undefined, own: Record<string, number>, from: number): number {
  const c = own[lab.id] ?? ms(lab.own_mark) ?? ms(accountMark) ?? from
  return Math.max(c, from)
}

// cutFor: where Crucible starts counting a lab, as the server does: its marker, never later than the last read of the
// bill (`read`); before any read, no mark counts.
export function cutFor(lab: UnbilledLab, accountMark: string | undefined, own: Record<string, number>, from: number, read: string | undefined): number {
  return read ? Math.min(markOf(lab, accountMark, own, from), Date.parse(read)) : from
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
    const cut = cutFor(l, accountMark, own, Date.parse(w.from), w.read_at)
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

// snap and step land on clock quarter-hours (:00, :15, :30, :45), not on a grid from the window's start, which moves on
// every read: a saved mark stays where it is.
export const snap = (t: number, from: number, to: number) => Math.min(Math.max(Math.round(t / Q) * Q, from), to)
export const step = (t: number, dir: -1 | 1, from: number, to: number) =>
  Math.min(Math.max(dir > 0 ? Math.floor(t / Q) * Q + Q : Math.ceil(t / Q) * Q - Q, from), to)
export const at = (t: number, from: number, to: number) => ((t - from) / (to - from)) * 100
// hhmm: a time on the timeline, in the browser's zone.
export const hhmm = (t: number) => new Date(t).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' })

// A marker being moved: key '' is the account marker, else a lab's id; orig is its saved time.
export type Drag = { key: string; t: number; orig: number }
// What the gestures need from the timeline: the window, the lane's width in px, the drag shown now, and a save (which
// clears the drag only if it is still the one it saved).
export type MarkCtx = { from: number; to: number; width: () => number; get: () => Drag | null; set: (d: Drag | null) => void; save: (d: Drag) => void }

// markGestures turns pointer and key events on the markers into at most one save per move. A pointer moves a marker by
// how far it travels (a click saves nothing); keys save 400 ms after the last press, and pressing a key on another
// marker, or grabbing one, saves the pending move first.
export function markGestures() {
  let downX: number | undefined
  let timer: ReturnType<typeof setTimeout> | undefined
  const commit = (c: MarkCtx) => {
    clearTimeout(timer)
    timer = undefined
    const d = c.get()
    if (!d) return
    if (d.t === d.orig) c.set(null)
    else c.save(d)
  }
  return {
    down(c: MarkCtx, key: string, t: number, x: number) {
      if (timer !== undefined) commit(c)
      downX = x
      c.set({ key, t, orig: t })
    },
    move(c: MarkCtx, key: string, x: number) {
      const d = c.get(), w = c.width()
      if (downX === undefined || d?.key !== key || w <= 0) return
      c.set({ ...d, t: snap(d.orig + ((x - downX) / w) * (c.to - c.from), c.from, c.to) })
    },
    up(c: MarkCtx, key: string) {
      if (downX === undefined) return
      downX = undefined
      if (c.get()?.key === key) commit(c)
    },
    cancel(c: MarkCtx) {
      downX = undefined
      clearTimeout(timer)
      timer = undefined
      c.set(null)
    },
    key(c: MarkCtx, key: string, t: number, name: string): boolean {
      if (!NAV.includes(name)) return false
      if (timer !== undefined && c.get()?.key !== key) commit(c)
      const cur = c.get()
      const d = cur?.key === key ? cur : { key, t, orig: t }
      const next = name === 'Home' ? c.from : name === 'End' ? c.to : step(d.t, name === 'ArrowLeft' || name === 'ArrowDown' ? -1 : 1, c.from, c.to)
      clearTimeout(timer)
      timer = undefined
      c.set({ ...d, t: next })
      return true
    },
    keyUp(c: MarkCtx, name: string) {
      if (!NAV.includes(name)) return
      clearTimeout(timer)
      timer = setTimeout(() => commit(c), 400)
    },
    dispose() { clearTimeout(timer) },
  }
}
const NAV = ['ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown', 'Home', 'End']
