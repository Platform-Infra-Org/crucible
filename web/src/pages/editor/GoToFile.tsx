import { useState } from 'react'
import { matchFiles } from './model'

// GoToFile is Ctrl/Cmd+P: type part of a path, Enter opens the first match, arrows move, Escape closes.
export function GoToFile({ paths, onPick, onClose }: { paths: string[]; onPick: (p: string) => void; onClose: () => void }) {
  const [q, setQ] = useState('')
  const [i, setI] = useState(0)
  const hits = matchFiles(paths, q).slice(0, 50)
  const key = (e: React.KeyboardEvent) => {
    if (e.key === 'Escape') onClose()
    else if (e.key === 'ArrowDown') { e.preventDefault(); setI((x) => Math.min(x + 1, hits.length - 1)) }
    else if (e.key === 'ArrowUp') { e.preventDefault(); setI((x) => Math.max(x - 1, 0)) }
    else if (e.key === 'Enter' && hits[i]) onPick(hits[i])
  }
  return (
    <div className="goto" role="dialog" aria-modal="true" aria-label="Go to file">
      <input autoFocus aria-label="File name" aria-controls="goto-list" aria-activedescendant={hits[i] ? `goto-${i}` : undefined}
        value={q} onChange={(e) => { setQ(e.target.value); setI(0) }} onKeyDown={key} />
      <ul id="goto-list" role="listbox">
        {hits.map((p, n) => <li key={p} id={`goto-${n}`} role="option" aria-selected={n === i} onMouseDown={() => onPick(p)}>{p}</li>)}
      </ul>
    </div>
  )
}
