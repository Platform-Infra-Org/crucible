import { useState } from 'react'
import { pathProblem } from '../../lib/editLimits'

type Props = {
  paths: string[]; greyed: { path: string; reason: string }[]; changed: Set<string>; active: string; readOnly: boolean
  onOpen: (p: string) => void; onNew: (p: string) => void; onRename: (from: string, to: string) => void; onDelete: (p: string) => void
  onNewModule?: () => void
}

// Explorer is the training at the draft's base plus the draft's changes. Disallowed paths are greyed with the reason;
// training.yaml can be edited but never renamed or deleted.
export function Explorer({ paths, greyed, changed, active, readOnly, onOpen, onNew, onRename, onDelete, onNewModule }: Props) {
  const [mode, setMode] = useState<{ kind: 'new' } | { kind: 'rename'; path: string } | null>(null)
  const [value, setValue] = useState('')
  const problem = mode && value ? pathProblem(value) : undefined
  const done = (e: React.FormEvent) => {
    e.preventDefault()
    if (!mode || problem || !value) return
    if (mode.kind === 'new') onNew(value)
    else onRename(mode.path, value)
    setMode(null)
  }
  return (
    <div className="explorer" role="region" aria-label="Explorer">
      {!readOnly && (
        <p>
          <button className="ghost" onClick={() => { setMode({ kind: 'new' }); setValue(active.includes('/') ? active.slice(0, active.lastIndexOf('/') + 1) : 'modules/') }}>New file</button>
          {onNewModule && <> <button className="ghost" onClick={onNewModule}>New module</button></>}
        </p>
      )}
      {mode && (
        <form onSubmit={done}>
          <label>{mode.kind === 'new' ? 'Path of the new file' : `New path for ${mode.path}`}{' '}
            <input autoFocus value={value} onChange={(e) => setValue(e.target.value)} onKeyDown={(e) => e.key === 'Escape' && setMode(null)} />
          </label>{' '}
          <button type="submit" disabled={!value || !!problem}>{mode.kind === 'new' ? 'Create' : 'Rename'}</button>{' '}
          <button type="button" className="ghost" onClick={() => setMode(null)}>Cancel</button>
          <span role="status" className="muted"> {problem}</span>
        </form>
      )}
      <ul>
        {paths.map((p) => (
          <li key={p}>
            <button className={p === active ? '' : 'ghost'} aria-label={p} aria-current={p === active ? 'true' : undefined} onClick={() => onOpen(p)}>
              {p}{changed.has(p) && <span aria-hidden="true"> •</span>}
            </button>
            {changed.has(p) && <span className="sr-only"> (changed)</span>}
            {!readOnly && p !== 'training.yaml' && (
              <>
                {' '}<button className="ghost small" aria-label={`Rename ${p}`} onClick={() => { setMode({ kind: 'rename', path: p }); setValue(p) }}>Rename</button>
                {' '}<button className="ghost small" aria-label={`Delete ${p}`} onClick={() => window.confirm(`Delete ${p}?`) && onDelete(p)}>Delete</button>
              </>
            )}
          </li>
        ))}
        {greyed.map((g) => (
          <li key={g.path}><span className="muted" title={g.reason} aria-label={`${g.path} (can't be edited here: ${g.reason})`}>{g.path}</span></li>
        ))}
      </ul>
    </div>
  )
}
