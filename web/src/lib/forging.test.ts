import { expect, test } from 'vitest'
import { CYCLE, IMPACT, LAST, STAGES, next, sparks } from './forging'

test('the stages are the forge ranks, in order, and the loop returns to ore after the masterwork', () => {
  expect(STAGES.map((s) => s.rank)).toEqual(['Ore', 'Ingot', 'Tempered', 'Blade', 'Sword', 'Masterwork'])
  expect([0, 1, 2, 3, 4, 5].map(next)).toEqual([1, 2, 3, 4, 5, 0])
  expect(LAST).toBe(5)
})

test('the hammer lands at 70% of its swing, where the CSS keyframe puts the impact', () => {
  expect(IMPACT / CYCLE).toBeCloseTo(0.7, 5)
})

test('sparks fly up and out on both sides of the strike', () => {
  const s = sparks()
  expect(s).toHaveLength(12)
  expect(s.every((p) => p.dy < 0)).toBe(true) // never into the anvil
  expect(s.filter((p) => p.dx < 0).length).toBeGreaterThan(3)
  expect(s.filter((p) => p.dx > 0).length).toBeGreaterThan(3)
  expect(sparks()).toEqual(s) // the same every render
})
