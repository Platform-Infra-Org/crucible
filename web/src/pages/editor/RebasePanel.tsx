import { useEffect, useRef, useState } from 'react'
import { monaco } from './monaco'
import { languageOf, monacoTheme } from './model'
import { canKeep, type Choice } from './rebase'
import type { Conflict } from '../../types'

// RebasePanel resolves files changed both in the draft and upstream. Left: the file now; right: yours, editable.
export function RebasePanel({ conflicts, onDone, onCancel }: { conflicts: Conflict[]; onDone: (choices: Choice[]) => void; onCancel: () => void }) {
  const [choices, setChoices] = useState<(Choice | undefined)[]>(conflicts.map(() => undefined))
  const [i, setI] = useState(0)
  const host = useRef<HTMLDivElement>(null)
  const diff = useRef<monaco.editor.IStandaloneDiffEditor | null>(null)
  const c = conflicts[i]
  useEffect(() => {
    if (!host.current || c.op.op !== 'put') return
    const d = monaco.editor.createDiffEditor(host.current, { automaticLayout: true, originalEditable: false, renderSideBySide: true, theme: monacoTheme(document.documentElement.dataset.theme), accessibilitySupport: 'on' })
    d.setModel({ original: monaco.editor.createModel(c.head, languageOf(c.path)), modified: monaco.editor.createModel(c.mine, languageOf(c.path)) })
    diff.current = d
    return () => { const m = d.getModel(); diff.current = null; d.dispose(); m?.original.dispose(); m?.modified.dispose() }
  }, [c])
  const choose = (ch: Choice) => {
    const next = choices.map((x, n) => (n === i ? ch : x))
    setChoices(next)
    if (next.every((x) => x !== undefined)) onDone(next as Choice[])
    else setI(next.findIndex((x) => x === undefined))
  }
  const what = c.op.op === 'put' ? (c.head_missing ? 'You changed it; it was deleted or moved upstream.' : 'Changed both here and upstream.')
    : c.op.op === 'delete' ? 'You deleted it; it changed upstream.' : c.path === c.op.to ? `You renamed ${c.op.from} to it; it now exists upstream.` : `You renamed it to ${c.op.to}; it changed upstream.`
  return (
    <section className="rebase" aria-label="Resolve newer content">
      <h2>Newer content: {i + 1} of {conflicts.length}</h2>
      <p><code>{c.path}</code>: {what}</p>
      {c.op.op === 'put' && <div ref={host} className="code-editor" data-testid="rebase-diff" aria-label={`Upstream on the left, your version on the right, editable: ${c.path}`} />}
      <p>
        {c.op.op === 'put' && <button onClick={() => choose({ put: diff.current?.getModel()?.modified.getValue() ?? c.mine })}>Use the right side</button>}{' '}
        {c.op.op !== 'put' && <button disabled={!canKeep(c)} title={canKeep(c) ? '' : 'The file it acts on is gone upstream'} onClick={() => choose('keep')}>Keep my change</button>}{' '}
        <button className="ghost" onClick={() => choose('drop')}>Drop my change (take theirs)</button>{' '}
        <button className="ghost" onClick={onCancel}>Cancel</button>
      </p>
    </section>
  )
}
