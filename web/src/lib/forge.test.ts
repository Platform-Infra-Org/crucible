import { expect, test } from 'vitest'
import { article } from './forge'

test('article agrees with the rank name', () => {
  expect(article('Apprentice')).toBe('an')
  expect(article('Ingot')).toBe('an')
  expect(article('Ore')).toBe('an')
  expect(article('Blade')).toBe('a')
  expect(article('Master Smith')).toBe('a')
})

import { nextBanner, seenBody } from './forge'

const up = {
  smith: { level: 2, title: 'Journeyman' },
  swords: [
    { training: 'a-101', title: 'A 101', level: 3, rank: 'Blade' },
    { training: 'b-102', title: 'B 102', level: 1, rank: 'Ingot' },
  ],
}

test('the smith rise comes first, then the swords in server order, skipping what is dismissed', () => {
  expect(nextBanner(up, new Set())?.key).toBe('smith')
  expect(nextBanner(up, new Set(['smith']))?.key).toBe('sword:a-101')
  expect(nextBanner(up, new Set(['smith', 'sword:a-101']))?.key).toBe('sword:b-102')
  expect(nextBanner(up, new Set(['smith', 'sword:a-101', 'sword:b-102']))).toBeNull()
  expect(nextBanner({ smith: null, swords: up.swords }, new Set())?.key).toBe('sword:a-101')
  expect(nextBanner(undefined, new Set())).toBeNull()
})

test('the seen call names the kind, the training (empty for smith) and the level', () => {
  expect(seenBody(nextBanner(up, new Set())!)).toEqual({ kind: 'smith', training: '', level: 2 })
  expect(seenBody(nextBanner(up, new Set(['smith']))!)).toEqual({ kind: 'sword', training: 'a-101', level: 3 })
})
