import { expect, test } from 'vitest'
import { average, group, options, pick } from './journey'
import type { JourneyRow } from '../types'

const row = (email: string, name: string, training: string, title: string, percent: number): JourneyRow =>
  ({ email, name, team: 'forge', training, title, percent, cells: [], flags: [] })
const rows = [row('z@x', 'Zed', 'f101', 'Forge 101', 40), row('a@x', 'Ada', 'f201', 'Forge 201', 10), row('a@x', 'Ada', 'f101', 'Forge 101', 90)]

test('by person: one group per person, by name, their trainings by title', () => {
  const g = group(rows, 'person')
  expect(g.map((x) => x.key)).toEqual(['a@x', 'z@x'])
  expect(g[0].rows.map((r) => r.title)).toEqual(['Forge 101', 'Forge 201'])
})

test('by training: one group per training, by title, its people by name', () => {
  const g = group(rows, 'training')
  expect(g.map((x) => x.key)).toEqual(['f101', 'f201'])
  expect(g[0].rows.map((r) => r.name)).toEqual(['Ada', 'Zed'])
})

test('average forged, and none for nobody', () => {
  expect(average(rows)).toBe(47)
  expect(average([])).toBe(0)
})

test('filters narrow by person, by training and to those who need a look; unset filters keep everyone', () => {
  const flagged = { ...rows[1], flags: [{ kind: 'inactive' as const, detail: 'quiet' }] }
  const all = [rows[0], flagged, rows[2]]
  expect(pick(all, { person: '', training: '', look: false })).toHaveLength(3)
  expect(pick(all, { person: 'a@x', training: '', look: false }).map((r) => r.training)).toEqual(['f201', 'f101'])
  expect(pick(all, { person: '', training: 'f101', look: false }).map((r) => r.email)).toEqual(['z@x', 'a@x'])
  expect(pick(all, { person: 'a@x', training: 'f101', look: false })).toHaveLength(1)
  expect(pick(all, { person: '', training: '', look: true }).map((r) => r.training)).toEqual(['f201'])
})

test('the pickers list each person and training once, by name and title', () => {
  expect(options(rows)).toEqual({
    people: [{ value: 'a@x', label: 'Ada' }, { value: 'z@x', label: 'Zed' }],
    trainings: [{ value: 'f101', label: 'Forge 101' }, { value: 'f201', label: 'Forge 201' }],
  })
})
