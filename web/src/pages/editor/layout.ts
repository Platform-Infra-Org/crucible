// Pane widths of the editor, in px. The editor takes the rest and never goes under MIN.editor; `room` is the width the
// side panel, editor and preview share (the container without the activity bar and splitters).
export type Layout = { side: number; preview: number; previewOpen: boolean }
type Pane = 'side' | 'preview'

export const MIN = { side: 180, editor: 320, preview: 240 }
export const DEFAULT_LAYOUT: Layout = { side: 260, preview: 440, previewOpen: true }
export const LAYOUT_KEY = 'crucible.ide.layout'

const clamp = (n: number, lo: number, hi: number) => Math.min(Math.max(n, lo), Math.max(lo, hi))

// fit is the widths shown: when they don't fit, the preview gives way first, then the side panel, down to their minimums.
export function fit(l: Layout, room: number, shown: boolean): { side: number; preview: number } {
  let side = l.side
  let preview = shown ? l.preview : 0
  let over = side + preview + MIN.editor - room
  if (over > 0 && shown) { const d = Math.max(0, Math.min(over, preview - MIN.preview)); preview -= d; over -= d }
  if (over > 0) side -= Math.max(0, Math.min(over, side - MIN.side))
  return { side, preview }
}

export function bounds(l: Layout, room: number, shown: boolean, pane: Pane): { min: number; max: number } {
  const f = fit(l, room, shown)
  const min = MIN[pane]
  return { min, max: Math.max(min, room - MIN.editor - (pane === 'side' ? f.preview : f.side)) }
}

export function resize(l: Layout, room: number, shown: boolean, pane: Pane, to: number): Layout {
  const b = bounds(l, room, shown, pane)
  return { ...l, [pane]: Math.round(clamp(to, b.min, b.max)) }
}

export function parseLayout(raw: string | null): Layout {
  try {
    const v = JSON.parse(raw ?? '')
    const num = (x: unknown, d: number, min: number) => (typeof x === 'number' && Number.isFinite(x) ? Math.max(min, Math.round(x)) : d)
    return { side: num(v.side, DEFAULT_LAYOUT.side, MIN.side), preview: num(v.preview, DEFAULT_LAYOUT.preview, MIN.preview), previewOpen: v.previewOpen !== false }
  } catch {
    return DEFAULT_LAYOUT
  }
}
