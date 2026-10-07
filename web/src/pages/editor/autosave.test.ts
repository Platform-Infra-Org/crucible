import { expect, test } from 'vitest'
import { ApiError } from '../../api'
import { afterSave, canAutosave, saveLabel } from './autosave'

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
