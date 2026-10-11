import { expect, test } from 'vitest'
import { article } from './forge'

test('article agrees with the rank name', () => {
  expect(article('Apprentice')).toBe('an')
  expect(article('Ingot')).toBe('an')
  expect(article('Ore')).toBe('an')
  expect(article('Blade')).toBe('a')
  expect(article('Master Smith')).toBe('a')
})
