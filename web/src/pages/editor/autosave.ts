import type { ApiError } from '../../api'

export type SaveState =
  | { kind: 'saved'; at: number }
  | { kind: 'dirty' }
  | { kind: 'saving' }
  | { kind: 'blocked'; reason: string } // the draft can't be saved as it is (too big, a bad path): fixing it resumes autosave
  | { kind: 'conflict'; reason: string } // another tab saved, or the draft went into review: reload; autosave stops
  | { kind: 'offline'; reason: string } // network or server trouble: the next change tries again

export function afterSave(err: ApiError | undefined, now: number): SaveState {
  if (!err) return { kind: 'saved', at: now }
  if (err.status === 409) return { kind: 'conflict', reason: err.message }
  if (err.status === 400 || err.status === 413) return { kind: 'blocked', reason: err.message }
  return { kind: 'offline', reason: err.message }
}

export const canAutosave = (s: SaveState) => s.kind !== 'conflict'

export function saveLabel(s: SaveState, now: number): string {
  switch (s.kind) {
    case 'saved': {
      const sec = Math.max(0, Math.round((now - s.at) / 1000))
      return sec < 5 ? 'Draft saved just now' : `Draft saved ${sec < 60 ? `${sec}s` : `${Math.round(sec / 60)}m`} ago`
    }
    case 'dirty':
      return 'Unsaved changes'
    case 'saving':
      return 'Saving…'
    case 'blocked':
    case 'conflict':
      return `Not saved: ${s.reason}`
    case 'offline':
      return `Not saved yet: ${s.reason}`
  }
}
