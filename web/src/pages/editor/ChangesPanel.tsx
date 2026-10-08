import { DiffView } from '../../components/DiffView'
import type { Change } from './model'

const label = { added: 'Added', changed: 'Changed', renamed: 'Renamed', deleted: 'Deleted' }

// ChangesPanel shows what a reviewer will see, with the same diff view (hidden and control characters marked).
export function ChangesPanel({ changes, diffOf, onOpen }: { changes: Change[]; diffOf: (c: Change) => string | undefined; onOpen: (p: string) => void }) {
  if (changes.length === 0) return <p className="muted">No changes yet.</p>
  return (
    <ul className="changes" aria-label="Changes">
      {changes.map((c) => {
        const diff = diffOf(c)
        return (
          <li key={c.kind + c.path}>
            <details>
              <summary>{label[c.kind]} <code>{c.from ? `${c.from} → ${c.path}` : c.path}</code></summary>
              {c.kind !== 'deleted' && <button className="ghost small" onClick={() => onOpen(c.path)}>Open</button>}
              {diff === undefined ? <p className="muted">Loading the original…</p> : <DiffView diff={diff} />}
            </details>
          </li>
        )
      })}
    </ul>
  )
}
