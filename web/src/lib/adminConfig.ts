import type { Schedule } from '../types'

export type Tiers = { auto_approve_usd: number; tier1_usd: number; tier2_usd: number }

const usd = (n: number) => `$${n}`

// tiersRequest reads the three tier boxes. All empty = unset (null); otherwise they must climb in order.
export function tiersRequest(auto: string, t1: string, t2: string): { tiers: Tiers | null; problem?: string } {
  if (!auto.trim() && !t1.trim() && !t2.trim()) return { tiers: null }
  const [a, b, c] = [auto, t1, t2].map((s) => (s.trim() === '' ? NaN : Number(s)))
  if ([a, b, c].some(Number.isNaN)) return { tiers: null, problem: 'Fill in all three cost lines, or clear all three to leave them unset.' }
  if (a < 0) return { tiers: null, problem: 'Auto-approve under cannot be negative.' }
  if (b <= 0) return { tiers: null, problem: 'The first approval line must be more than $0.' }
  if (a > b) return { tiers: null, problem: `Auto-approve under ${usd(a)} cannot be more than the first approval line (${usd(b)}).` }
  if (b > c) return { tiers: null, problem: `The first approval line (${usd(b)}) cannot be more than the second (${usd(c)}).` }
  return { tiers: { auto_approve_usd: a, tier1_usd: b, tier2_usd: c } }
}

// repoWarning is shown before a repoint is sent. A branch-only change, or a repo nobody is enrolled in, needs none.
export function repoWarning(oldRepo: string | undefined, newRepo: string, programs: number): string {
  if (oldRepo === undefined || oldRepo === newRepo.trim() || programs === 0) return ''
  return `${programs} program${programs === 1 ? ' is' : 's are'} pinned to content from the old repository. Those pins may stop resolving once this training points somewhere else.`
}

// windowsRequest reads lines like "mon,tue,wed 08:00-19:00" into schedule windows.
export function windowsRequest(text: string): { windows: Schedule['windows']; problem?: string } {
  const windows: Schedule['windows'] = []
  for (const line of text.split('\n').map((l) => l.trim()).filter(Boolean)) {
    const m = /^([a-z, ]+?)\s+(\d{1,2}:\d{2})\s*-\s*(\d{1,2}:\d{2})$/i.exec(line)
    if (!m) return { windows, problem: `Window lines look like "mon,tue,wed 08:00-19:00": ${line}` }
    windows.push({ days: m[1].split(/[,\s]+/).filter(Boolean).map((d) => d.toLowerCase()), start: m[2], end: m[3] })
  }
  if (windows.length === 0) return { windows, problem: 'Add at least one window, such as "mon,tue,wed,thu,fri 08:00-19:00".' }
  return { windows }
}
