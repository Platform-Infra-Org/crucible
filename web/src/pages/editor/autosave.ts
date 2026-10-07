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

// resolving: newer content is being resolved; the editor's ops predate the resolution and must not be written.
export const canAutosave = (s: SaveState, resolving = false) => s.kind !== 'conflict' && !resolving

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

// serialSaves runs saves (and the submit) one at a time. Each starts from the draft the previous one returned, so a slow
// save never sends a stale updated_at and turns into a self-inflicted "changed in another tab" 409.
export function serialSaves<T>() {
  let last: T | undefined
  let tail: Promise<unknown> = Promise.resolve()
  return {
    get: () => last,
    set: (d: T) => { last = d },
    run<R extends T>(job: (d: T) => Promise<R>): Promise<R> {
      const p = tail.then(() => job(last as T)).then((d) => { last = d; return d })
      tail = p.catch(() => undefined)
      return p
    },
  }
}

// onLeave decides what leaving the editor inside the app does with work the server doesn't have yet. flush: send the
// latest draft as it unmounts (a pending autosave, or a retry after a network failure). ask: confirm before following a
// link, because that save can't succeed or may fail again. frozen: read-only, or newer content being resolved: the
// editor's ops must not be written. unsaveable: the draft breaks a limit (the server would refuse it).
export function onLeave(s: SaveState, frozen: boolean, unsaveable: boolean): { flush: boolean; ask: boolean } {
  if (frozen || s.kind === 'conflict' || s.kind === 'saved' || s.kind === 'saving') return { flush: false, ask: false }
  if (s.kind === 'blocked' || unsaveable) return { flush: false, ask: true }
  return { flush: true, ask: s.kind === 'offline' }
}
