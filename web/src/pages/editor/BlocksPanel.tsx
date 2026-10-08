import { useEffect, useState } from 'react'
import type { BlockInfo } from '../../types'
import { formProblem, initialValues, labOf, modulesOf, tasksOf } from './forms'

type Props = {
  groups: string[]; blocks: BlockInfo[]; paths: string[]; read: (p: string) => string | undefined; need: (p: string) => void
  preselect?: string; readOnly?: boolean; onInsert: (block: BlockInfo, values: Record<string, string>) => Promise<void>
}

// BlocksPanel: pick a block, fill the form generated from its registry fields, and the server writes the files.
// ponytail: preselect is read on mount only; Ide mounts the panel afresh each time it opens it.
export function BlocksPanel({ groups, blocks, paths, read, need, preselect, readOnly, onInsert }: Props) {
  const [pick, setPick] = useState(preselect ?? '')
  const block = blocks.find((b) => b.id === pick)
  const [values, setValues] = useState<Record<string, string>>(() => (block ? initialValues(block.fields) : {}))
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const module = values.module ?? ''
  const lab = labOf(paths, module)
  useEffect(() => { if (lab) need(lab) }, [lab, need]) // the task picker reads the module's lab.yaml

  if (!block) {
    return (
      <div className="blocks" role="region" aria-label="Blocks">
        {groups.map((g) => (
          <div key={g}>
            <h3>{g}</h3>
            <ul>
              {blocks.filter((b) => b.group === g).map((b) => (
                <li key={b.id}>
                  <button className="ghost" onClick={() => { setPick(b.id); setValues(initialValues(b.fields)); setErr('') }}>{b.title}</button>
                  <span className="muted"> {b.summary}</span>
                </li>
              ))}
            </ul>
          </div>
        ))}
      </div>
    )
  }
  const problem = formProblem(block.fields, values)
  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (problem) return setErr(problem)
    setBusy(true); setErr('')
    try { await onInsert(block, values) } catch (x) { setErr((x as Error).message) } finally { setBusy(false) }
  }
  return (
    <div className="blocks" role="region" aria-label={`Block: ${block.title}`}>
      <p><button className="ghost" onClick={() => setPick('')}>← All blocks</button></p>
      <h3>{block.title}</h3>
      <p>{block.summary}</p>
      {block.doc && <p className="muted">{block.doc}</p>}
      <p><a href={`/docs/authors/blocks/${block.group.toLowerCase()}`} target="_blank" rel="noreferrer">{`Reference: ${block.group} blocks`}</a></p>
      <pre aria-label="Example">{block.example}</pre>
      {block.git_only ? <p role="note">Add this in git: this block can't be added in the browser.</p> : (
        <form onSubmit={submit}>
          {block.fields.map((f) => {
            const common = { id: `bf-${f.name}`, value: values[f.name] ?? '', 'aria-required': f.required ? true : undefined, 'aria-describedby': `bf-${f.name}-help`,
              onChange: (e: React.ChangeEvent<HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement>) => setValues((v) => ({ ...v, [f.name]: e.target.value })) }
            const choices = f.type === 'module' ? modulesOf(paths) : f.type === 'task' ? tasksOf(paths, module, read) : f.type === 'enum' ? f.enum ?? [] : f.type === 'bool' ? ['false', 'true'] : undefined
            return (
              <p key={f.name}>
                <label htmlFor={`bf-${f.name}`}>{f.name}{f.required && ' *'}</label><br />
                {choices ? (
                  <select {...common}><option value="">Choose…</option>{choices.map((c) => <option key={c} value={c}>{c}</option>)}</select>
                ) : ['text', 'list', 'pairs'].includes(f.type) ? <textarea rows={3} {...common} /> : <input {...common} />}
                <br /><small id={`bf-${f.name}-help`} className="muted">{f.description}</small>
              </p>
            )
          })}
          <p><button type="submit" disabled={busy || readOnly}>Add to the draft</button> <span role="alert" className="error">{err}</span></p>
        </form>
      )}
    </div>
  )
}
