import { expect, test } from 'vitest'
import { formProblem, initialValues, modulesOf, tasksOf } from './forms'
import type { BlockField } from '../../types'

const fields: BlockField[] = [
  { name: 'module', type: 'module', required: true, description: 'Which module?' },
  { name: 'id', type: 'id', required: true, description: 'Id' },
  { name: 'points', type: 'number', description: 'Points', min: 0 },
  { name: 'progression', type: 'enum', enum: ['linear', 'free'], default: 'linear', description: 'Order' },
]

test('defaults fill the form', () => {
  expect(initialValues(fields)).toEqual({ module: '', id: '', points: '', progression: 'linear' })
})

test('the form mirrors the server checks', () => {
  expect(formProblem(fields, { module: '', id: 'x' })).toMatch(/module is required/)
  expect(formProblem(fields, { module: 'm1', id: 'a b' })).toMatch(/id/)
  expect(formProblem(fields, { module: 'm1', id: 'q1', points: '-1' })).toMatch(/points/)
  expect(formProblem(fields, { module: 'm1', id: 'q1', points: '2', progression: 'linear' })).toBeUndefined()
})

test('targets come from the draft: modules, and the tasks of a module lab', () => {
  const paths = ['training.yaml', 'modules/01-a/module.yaml', 'modules/01-a/lab/lab.yaml', 'modules/02-b/module.yaml']
  expect(modulesOf(paths)).toEqual(['01-a', '02-b'])
  const lab = 'id: l\nruntime: local\nterminals: [{name: s, service: s}]\ntasks:\n  - {id: t1, instructions: a.md}\n  - {id: t2, instructions: b.md}\n'
  expect(tasksOf(paths, '01-a', (p) => (p.endsWith('lab.yaml') ? lab : undefined))).toEqual(['t1', 't2'])
  expect(tasksOf(paths, '02-b', () => undefined)).toEqual([])
})
