import { ApiError } from '../api'
import { toast } from '../lib/alerts'

// reportSaveError toasts the failure; a 409 (git moved on) also returns true so the caller can offer Reload.
export function reportSaveError(err: unknown): boolean {
  const stale = err instanceof ApiError && err.status === 409
  if (!stale) toast((err as Error).message) // the 409 banner is announced by itself
  return stale
}

export function Conflict({ onReload }: { onReload: () => void }) {
  return (
    <div role="alert" className="conflict">
      <span>Someone changed this in git, reload to see the latest.</span>
      <button type="button" onClick={onReload}>Reload</button>
    </div>
  )
}
