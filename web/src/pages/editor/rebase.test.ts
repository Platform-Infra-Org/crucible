import { expect, test } from 'vitest'
import { canKeep, resolveOps, withBase } from './rebase'
import type { Conflict } from '../../types'

const put = { op: 'put' as const, path: 'm/a.md', content: 'mine' }
const del = { op: 'delete' as const, path: 'm/b.md' }
const ren = { op: 'rename' as const, from: 'm/c.md', to: 'm/d.md' }
const c = (op: Conflict['op'], path: string, head_missing = false): Conflict => ({ path, base: 'old', head: head_missing ? '' : 'theirs', head_missing, mine: op.op === 'put' ? op.content : '', op })

test('each conflict is kept, dropped or replaced by the merged text; untouched ops stay', () => {
  const other = { op: 'put' as const, path: 'm/z.md', content: 'z' }
  const ops = [ren, del, put, other]
  const conflicts = [c(put, 'm/a.md'), c(del, 'm/b.md'), c(ren, 'm/c.md')]
  expect(resolveOps(ops, conflicts, [{ put: 'merged' }, 'drop', 'keep'])).toEqual([ren, { ...put, content: 'merged' }, other])
})

test('a rename with two conflicts (source changed, target appeared) is dropped if either is dropped', () => {
  const conflicts = [c(ren, 'm/d.md'), c(ren, 'm/c.md')]
  expect(resolveOps([ren], conflicts, ['drop', 'keep'])).toEqual([])
  expect(resolveOps([ren], conflicts, ['keep', 'drop'])).toEqual([])
})

test('a rename or delete of a file that is gone upstream can only be dropped', () => {
  expect(canKeep(c(del, 'm/b.md', true))).toBe(false)
  expect(canKeep(c(ren, 'm/c.md', true))).toBe(false)
  expect(canKeep(c(put, 'm/a.md', true))).toBe(true) // keeping recreates the file
  expect(canKeep({ ...c(ren, 'm/d.md'), head_missing: false, base: '' })).toBe(false) // the target appeared upstream
})

test('a base text fetched for an older base is ignored once the draft moved to a newer one', () => {
  const now = { sha: 'new', text: { 'm/a.md': 'new a' } }
  expect(withBase(now, 'old', 'm/b.md', 'old b')).toBe(now) // a stale fetch that was in flight during a rebase
  expect(withBase(now, 'new', 'm/b.md', 'new b')).toEqual({ sha: 'new', text: { 'm/a.md': 'new a', 'm/b.md': 'new b' } })
})

test('dropping a rename moves the edit of its new name back to the old name, so it is neither lost nor a copy', () => {
  const edit = { op: 'put' as const, path: 'm/d.md', content: 'mine' }
  const conflicts = [c(ren, 'm/c.md'), { ...c(edit, 'm/d.md'), head: 'theirs' }]
  expect(resolveOps([ren, edit], conflicts, ['drop', { put: 'merged' }])).toEqual([{ ...edit, path: 'm/c.md', content: 'merged' }])
  expect(resolveOps([ren, edit], conflicts, ['keep', { put: 'merged' }])).toEqual([ren, { ...edit, content: 'merged' }])
  expect(resolveOps([ren, edit], conflicts, ['keep', 'drop'])).toEqual([ren]) // the renamed file takes upstream's text
})

// Review Focus 5: upstream deleted the file the draft renamed and edited. The rename can only be dropped; the edited text
// the author keeps stays, as a new file at the new name.
test('dropping a rename whose source is gone upstream keeps the edit of its new name as a new file', () => {
  const edit = { op: 'put' as const, path: 'm/d.md', content: 'mine' }
  const conflicts = [c(ren, 'm/c.md', true), c(edit, 'm/d.md', true)]
  expect(resolveOps([ren, edit], conflicts, ['drop', { put: 'kept' }])).toEqual([{ ...edit, content: 'kept' }])
  expect(resolveOps([ren, edit], conflicts, ['drop', 'drop'])).toEqual([])
})

test('a dropped rename whose old name the draft wrote again keeps the edit at the new name', () => {
  const edit = { op: 'put' as const, path: 'm/d.md', content: 'mine' }
  const again = { op: 'put' as const, path: 'm/c.md', content: 'new c' }
  expect(resolveOps([ren, edit, again], [c(ren, 'm/c.md')], ['drop'])).toEqual([edit, again])
})
