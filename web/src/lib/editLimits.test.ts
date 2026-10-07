import { expect, test } from 'vitest'
import { opsProblem, pathProblem } from './editLimits'

test('paths follow the server rules', () => {
  expect(pathProblem('modules/m1/reading/a.md')).toBeUndefined()
  expect(pathProblem('training.yaml')).toBeUndefined()
  for (const p of ['README.md', 'modules/a.md', 'modules/m1/.hidden.md', 'modules/m1/x .md', 'modules/m1/ä.md', 'modules/m1/../x.md', 'modules/m1/main.tf'])
    expect(pathProblem(p), p).toBeTruthy()
})

// Review Focus 1: a draft over the 1 MiB request cap is caught before autosave sends it.
test('a draft over 1 MiB is refused with a reason', () => {
  const big = Array.from({ length: 5 }, (_, i) => ({ op: 'put' as const, path: `modules/m1/reading/${i}.md`, content: 'x'.repeat(250_000) }))
  expect(opsProblem(big)).toMatch(/over 1 MiB/)
  expect(opsProblem(big.slice(0, 3))).toBeUndefined()
})

test('op rules', () => {
  expect(opsProblem([{ op: 'delete', path: 'training.yaml' }])).toMatch(/training\.yaml can't be renamed or deleted/)
  expect(opsProblem([{ op: 'rename', from: 'training.yaml', to: 'modules/m1/t.yaml' }])).toMatch(/training\.yaml can't be renamed or deleted/)
  expect(opsProblem(Array.from({ length: 21 }, (_, i) => ({ op: 'delete' as const, path: `modules/m1/${i}.md` })))).toMatch(/at most 20/)
  expect(opsProblem([{ op: 'put', path: 'modules/m1/a.md', content: 'a\0b' }])).toMatch(/256 KiB/)
})
