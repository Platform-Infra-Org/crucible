import { expect, test } from 'vitest'
import { CYCLE, IMPACT, LAST, RUNE_STEP, STAGES, bolt, next, sparks } from './forging'

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

test('the rune forge times every rank before the masterwork, which stays', () => {
  expect(RUNE_STEP).toHaveLength(LAST)
  expect(RUNE_STEP.every((ms) => ms > 1000)).toBe(true) // long enough for a rune, its beam and the change
})

test('a bolt is a closed jagged loop around the blade, the same every render', () => {
  const b = bolt(3)
  expect(b.startsWith('M')).toBe(true)
  expect(b.endsWith('Z')).toBe(true)
  expect(b.split('L')).toHaveLength(40)
  expect(bolt(3)).toBe(b)
  expect(bolt(5)).not.toBe(b)
})
