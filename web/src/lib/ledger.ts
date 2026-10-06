import type { Accuracy, Spend } from '../types'

// What a burn-down bar measures against: the hard cap if there is one, else the budget; 0 = neither.
export const limitOf = (s: Spend): number => s.cap_usd || s.budget_usd

// Bar heights in % of the busiest day, for a CSS bar row (no chart library).
export function barHeights(days: { estimate_usd: number; actual_usd: number }[]): { estimate: number; actual: number }[] {
  const top = Math.max(0, ...days.flatMap((d) => [d.estimate_usd, d.actual_usd]))
  return days.map((d) => (top === 0 ? { estimate: 0, actual: 0 } : {
    estimate: Math.round((d.estimate_usd / top) * 100),
    actual: Math.round((d.actual_usd / top) * 100),
  }))
}

// AWS's bill as a % of the estimate for settled labs; null until one has settled.
export const accuracyPct = (a: Accuracy): number | null =>
  a.labs === 0 || a.estimate_usd <= 0 ? null : Math.round((a.actual_usd / a.estimate_usd) * 100)
