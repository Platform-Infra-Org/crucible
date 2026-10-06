import { useEffect, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router'
import { api } from '../api'
import type { ContentEdit } from '../types'
import { Markdown } from '../components/Markdown'
import { editProblem } from '../lib/editLimits'

type FileList = { head_sha: string; files: { path: string; size: number }[] }

export function EditFilesPage() {
  const nav = useNavigate()
  const [q] = useSearchParams()
  const training = q.get('training') ?? ''
  const from = q.get('from')
  const [list, setList] = useState<FileList>()
  const [files, setFiles] = useState<Record<string, string>>({})
  const [orig, setOrig] = useState<Record<string, string>>({}) // as loaded from the head; seeded files have no entry
  const [open, setOpen] = useState('')
  const [title, setTitle] = useState('')
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    api<FileList>(`/api/content/${encodeURIComponent(training)}/files`).then(setList).catch((e: Error) => setErr(e.message))
  }, [training])
  useEffect(() => {
    if (!from) return
    api<ContentEdit>(`/api/edits/${encodeURIComponent(from)}`)
      .then((e) => { setTitle(e.title); setFiles(e.files ?? {}); setOpen(Object.keys(e.files ?? {})[0] ?? '') })
      .catch((e: Error) => setErr(e.message))
  }, [from])

  const openFile = async (p: string) => {
    setOpen(p)
    if (p in files) return
    try {
      const f = await api<{ content: string }>(`/api/content/${encodeURIComponent(training)}/file?path=${encodeURIComponent(p)}`)
      setOrig((o) => ({ ...o, [p]: f.content }))
      setFiles((s) => ({ ...s, [p]: f.content }))
    } catch (e) { setErr((e as Error).message) }
  }
  const changed = Object.fromEntries(Object.entries(files).filter(([p, t]) => orig[p] !== t))
  const problem = Object.keys(changed).length ? editProblem(changed) : undefined

  const submit = async () => {
    setErr('')
    const p = editProblem(changed)
    if (p) return setErr(p)
    if (!title.trim()) return setErr('Give the edit a title.')
    setBusy(true)
    try {
      const e = await api<ContentEdit>('/api/edits', { method: 'POST', json: { training, base_sha: list?.head_sha, title: title.trim(), files: changed } })
      nav(`/edits/${e.id}`)
    } catch (e) { setErr((e as Error).message) } finally { setBusy(false) }
  }

  return (
    <section className="page">
      <h1>Edit {training}</h1>
      {err && <p role="alert" className="error">{err}</p>}
      {problem && <p className="muted">{problem}</p>}
      <div className="editor">
        <div>
          <ul>
            {(list?.files ?? []).map((f) => (
              <li key={f.path}><button className={open === f.path ? '' : 'ghost'} onClick={() => openFile(f.path)}>{f.path}</button>{f.path in changed && ' *'}</li>
            ))}
          </ul>
        </div>
        {open && (
          <>
            <textarea aria-label={`Content of ${open}`} value={files[open] ?? ''} rows={24} spellCheck={false}
              onChange={(e) => setFiles((s) => ({ ...s, [open]: e.target.value }))} />
            <div data-testid="edit-preview">{open.endsWith('.md') ? <Markdown text={files[open] ?? ''} /> : <pre>{files[open] ?? ''}</pre>}</div>
          </>
        )}
      </div>
      <p>
        <label>Title <input value={title} maxLength={200} onChange={(e) => setTitle(e.target.value)} /></label>{' '}
        <button disabled={busy} onClick={submit}>Submit for review</button>
      </p>
    </section>
  )
}
