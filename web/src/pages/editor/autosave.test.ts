import { expect, test } from 'vitest'
import { ApiError } from '../../api'
import { afterSave, canAutosave, onLeave, saveLabel, serialSaves } from './autosave'

// Review Focus 2: when another tab saved, this tab stops autosaving and says why. No silent overwrite, no retry loop.
test('409 stops autosave; 400 blocks until fixed; other failures retry on the next change', () => {
  const conflict = afterSave(new ApiError(409, 'this draft changed in another tab or window; reload it'), 0)
  expect(conflict.kind).toBe('conflict')
  expect(canAutosave(conflict)).toBe(false)
  expect(saveLabel(conflict, 0)).toContain('another tab')
  expect(afterSave(new ApiError(400, 'the request is over 1 MiB; make it smaller'), 0).kind).toBe('blocked')
  expect(canAutosave(afterSave(new ApiError(503, 'unavailable'), 0))).toBe(true)
})

test('the saved label ages', () => {
  expect(saveLabel({ kind: 'saved', at: 0 }, 2_000)).toBe('Draft saved just now')
  expect(saveLabel({ kind: 'saved', at: 0 }, 30_000)).toBe('Draft saved 30s ago')
  expect(saveLabel({ kind: 'saved', at: 0 }, 180_000)).toBe('Draft saved 3m ago')
})

test('saves run one at a time; the next one starts from the updated_at the last one returned', async () => {
  const s = serialSaves<{ updated_at: string }>()
  s.set({ updated_at: 't0' })
  const sent: string[] = []
  let busy = false
  const put = async (d: { updated_at: string }) => {
    expect(busy).toBe(false) // never two requests in flight
    busy = true
    sent.push(d.updated_at)
    await new Promise((r) => setTimeout(r, 5)) // a slow save
    busy = false
    return { updated_at: d.updated_at + '+' }
  }
  const a = s.run(put)
  const b = s.run(put) // typed (or pressed Submit) while the first save is in flight
  expect((await b).updated_at).toBe('t0++')
  expect((await a).updated_at).toBe('t0+')
  expect(sent).toEqual(['t0', 't0+'])
})

test('a failed save does not jam the queue', async () => {
  const s = serialSaves<{ updated_at: string }>()
  s.set({ updated_at: 't0' })
  await expect(s.run(async () => { throw new Error('offline') })).rejects.toThrow('offline')
  expect((await s.run(async (d) => ({ updated_at: d.updated_at + '!' }))).updated_at).toBe('t0!')
})

// Fix round 1 (Task 13): while newer content is being resolved, or after a resolution whose reload failed, nothing may
// autosave: the editor's ops are from before the resolution and would be written at the new base.
test('no autosave while resolving a rebase', () => {
  expect(canAutosave({ kind: 'saved', at: 0 }, true)).toBe(false)
  expect(canAutosave({ kind: 'saved', at: 0 }, false)).toBe(true)
})

// Review I-1: leaving through an in-app link never silently drops work the server doesn't have.
test('leaving the editor saves pending work, and asks first when that save cannot succeed', () => {
  const dirty = { kind: 'dirty' } as const
  const offline = { kind: 'offline', reason: 'unavailable' } as const
  const blocked = { kind: 'blocked', reason: 'too big' } as const
  expect(onLeave(dirty, false, false)).toEqual({ flush: true, ask: false }) // typed within the autosave pause
  expect(onLeave(offline, false, false)).toEqual({ flush: true, ask: true }) // try once more, but say it may be lost
  expect(onLeave(blocked, false, false)).toEqual({ flush: false, ask: true })
  expect(onLeave(dirty, false, true)).toEqual({ flush: false, ask: true }) // over a limit before the autosave said so
  expect(onLeave({ kind: 'saved', at: 0 }, false, false)).toEqual({ flush: false, ask: false })
  expect(onLeave({ kind: 'saving' }, false, false)).toEqual({ flush: false, ask: false }) // the save in flight finishes
  expect(onLeave({ kind: 'conflict', reason: 'another tab' }, false, false)).toEqual({ flush: false, ask: false })
  expect(onLeave(dirty, true, false)).toEqual({ flush: false, ask: false }) // read-only or resolving: never written
})
