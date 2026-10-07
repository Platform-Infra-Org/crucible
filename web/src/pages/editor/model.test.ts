import { expect, test } from 'vitest'
import { changeList, currentPaths, deletePath, emptyOps, fromOps, languageOf, matchFiles, monacoTheme, origin, putText, renamePath, toOps } from './model'

const base = ['training.yaml', 'modules/m1/module.yaml', 'modules/m1/reading/a.md', 'modules/m1/reading/b.md']

test('ops round-trip in the order the server applies them', () => {
  const ops = [
    { op: 'put' as const, path: 'modules/m1/reading/c.md', content: 'C' },
    { op: 'delete' as const, path: 'modules/m1/reading/b.md' },
    { op: 'rename' as const, from: 'modules/m1/reading/a.md', to: 'modules/m1/reading/z.md' },
  ]
  expect(toOps(fromOps(ops)).map((o) => o.op)).toEqual(['rename', 'delete', 'put'])
  expect(currentPaths(base, fromOps(ops))).toEqual(['modules/m1/module.yaml', 'modules/m1/reading/c.md', 'modules/m1/reading/z.md', 'training.yaml'])
})

test('a file edited back to its original leaves the draft', () => {
  let d = putText(emptyOps(), 'modules/m1/reading/a.md', 'A2', 'A')
  expect(d.puts).toEqual({ 'modules/m1/reading/a.md': 'A2' })
  d = putText(d, 'modules/m1/reading/a.md', 'A', 'A')
  expect(toOps(d)).toEqual([])
})

test('rename keeps unsaved text, chains collapse, renaming back undoes it', () => {
  let d = putText(emptyOps(), 'modules/m1/reading/a.md', 'A2', 'A')
  d = renamePath(base, d, 'modules/m1/reading/a.md', 'modules/m1/reading/x.md')
  d = renamePath(base, d, 'modules/m1/reading/x.md', 'modules/m2/reading/y.md')
  expect(d.renames).toEqual({ 'modules/m2/reading/y.md': 'modules/m1/reading/a.md' })
  expect(d.puts).toEqual({ 'modules/m2/reading/y.md': 'A2' })
  expect(origin(base, d, 'modules/m2/reading/y.md')).toBe('modules/m1/reading/a.md')
  expect(origin(base, d, 'modules/m1/reading/a.md')).toBeUndefined()
  d = renamePath(base, d, 'modules/m2/reading/y.md', 'modules/m1/reading/a.md')
  expect(d.renames).toEqual({})
  expect(() => renamePath(base, d, 'modules/m1/reading/a.md', 'modules/m1/reading/b.md')).toThrow(/already exists/)
})

test('renaming a new file moves its put; deleting it forgets it', () => {
  let d = putText(emptyOps(), 'modules/m1/reading/n.md', 'N', undefined)
  d = renamePath(base, d, 'modules/m1/reading/n.md', 'modules/m1/reading/m.md')
  expect(toOps(d)).toEqual([{ op: 'put', path: 'modules/m1/reading/m.md', content: 'N' }])
  expect(toOps(deletePath(base, d, 'modules/m1/reading/m.md'))).toEqual([])
})

test('deleting a renamed file deletes its original; a deleted name is not reused by a rename', () => {
  let d = renamePath(base, emptyOps(), 'modules/m1/reading/a.md', 'modules/m1/reading/x.md')
  d = deletePath(base, d, 'modules/m1/reading/x.md')
  expect(toOps(d)).toEqual([{ op: 'delete', path: 'modules/m1/reading/a.md' }])
  expect(() => renamePath(base, d, 'modules/m1/reading/b.md', 'modules/m1/reading/a.md')).toThrow(/deleted/)
})

test('changes list what a reviewer will see', () => {
  let d = renamePath(base, emptyOps(), 'modules/m1/reading/a.md', 'modules/m1/reading/x.md')
  d = deletePath(base, d, 'modules/m1/reading/b.md')
  d = putText(d, 'modules/m1/module.yaml', 'title: M\n', 'title: M1\n')
  d = putText(d, 'modules/m1/reading/new.md', '# New\n', undefined)
  expect(changeList(base, d)).toEqual([
    { kind: 'changed', path: 'modules/m1/module.yaml' },
    { kind: 'deleted', path: 'modules/m1/reading/b.md' },
    { kind: 'added', path: 'modules/m1/reading/new.md' },
    { kind: 'renamed', path: 'modules/m1/reading/x.md', from: 'modules/m1/reading/a.md' },
  ])
})

test('go to file matches in order, best name first', () => {
  expect(matchFiles(base, 'rdb')).toEqual(['modules/m1/reading/b.md'])
  expect(matchFiles(base, 'module')[0]).toBe('modules/m1/module.yaml')
  expect(matchFiles(base, '')).toEqual(base)
})

test('languages and themes', () => {
  expect([languageOf('a.md'), languageOf('a.sh'), languageOf('a.yml'), languageOf('a.yaml')]).toEqual(['markdown', 'shell', 'yaml', 'yaml'])
  expect([monacoTheme('forge'), monacoTheme('quench'), monacoTheme('contrast'), monacoTheme('anvil')]).toEqual(['vs-dark', 'vs-dark', 'hc-black', 'vs'])
})

test('deleting a file and creating one at the same path is one put, never delete+put of a path', () => {
  let d = deletePath(base, emptyOps(), 'modules/m1/reading/a.md')
  d = putText(d, 'modules/m1/reading/a.md', '', undefined)
  expect(toOps(d)).toEqual([{ op: 'put', path: 'modules/m1/reading/a.md', content: '' }])
  expect(origin(base, d, 'modules/m1/reading/a.md')).toBe('modules/m1/reading/a.md')
  expect(changeList(base, d)).toEqual([{ kind: 'changed', path: 'modules/m1/reading/a.md' }])
})

test('reopening an edit keeps its renames and deletes', () => {
  const ops = [
    { op: 'rename' as const, from: 'modules/m1/reading/a.md', to: 'modules/m1/reading/z.md' },
    { op: 'delete' as const, path: 'modules/m1/reading/b.md' },
  ]
  expect(toOps(fromOps(ops))).toEqual(ops)
})
