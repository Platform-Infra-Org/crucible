import { describe, expect, it } from 'vitest'
import { accuracyPct, barHeights, limitOf } from './ledger'

describe('ledger helpers', () => {
  it('measures burn against the hard cap, else the budget', () => {
    expect(limitOf({ spent_usd: 1, committed_usd: 1, actual_usd: 0, budget_usd: 200, cap_usd: 250 })).toBe(250)
    expect(limitOf({ spent_usd: 1, committed_usd: 1, actual_usd: 0, budget_usd: 200, cap_usd: 0 })).toBe(200)
    expect(limitOf({ spent_usd: 1, committed_usd: 1, actual_usd: 0, budget_usd: 0, cap_usd: 0 })).toBe(0)
  })
  it('scales bars to the busiest day and survives an empty month', () => {
    expect(barHeights([{ estimate_usd: 2, actual_usd: 1 }, { estimate_usd: 0, actual_usd: 4 }])).toEqual([
      { estimate: 50, actual: 25 },
      { estimate: 0, actual: 100 },
    ])
    expect(barHeights([{ estimate_usd: 0, actual_usd: 0 }])).toEqual([{ estimate: 0, actual: 0 }])
  })
  it('reports accuracy only once something has settled', () => {
    expect(accuracyPct({ labs: 0, estimate_usd: 0, actual_usd: 0 })).toBeNull()
    expect(accuracyPct({ labs: 2, estimate_usd: 2, actual_usd: 0.4 })).toBe(20)
  })
})
