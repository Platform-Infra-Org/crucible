import { useEffect, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router'
import { api } from '../api'
import type { ContentEdit, FileEntry } from '../types'
import { Loader } from '../components/Loader'
import { Markdown } from '../components/Markdown'
import { DiffView } from '../components/DiffView'
import { editProblem } from '../lib/editLimits'
import { byteLen, changedFiles, headVersions } from '../lib/editDraft'

type FileList = { head_sha: string; files: FileEntry[] }

export function EditFilesPage() {
  const nav = useNavigate()
  const [q] = useSearchParams()
  const training = q.get('training') ?? ''
  const from = q.get('from')
  const [list, setList] = useState<FileList>()
  const [files, setFiles] = useState<Record<string, string>>({})
  const [orig, setOrig] = useState<Record<string, string>>({}) // as loaded from the head
  const [open, setOpen] = useState('')
  const [title, setTitle] = useState('')
  const [err, setErr] = useState('')
  const [old, setOld] = useState<ContentEdit>() // the stale edit being redone, for reference only
  const [busy, setBusy] = useState(false)

  const loadFile = async (p: string) =>
    (await api<{ content: string }>(`/api/content/${encodeURIComponent(training)}/file?path=${encodeURIComponent(p)}`)).content
  useEffect(() => {
    api<FileList>(`/api/content/${encodeURIComponent(training)}/files`).then(setList).catch((e: Error) => setErr(e.message))
  }, [training])
  useEffect(() => {
    if (!from) return
    // Redo starts from the CURRENT head text of the same paths: the old text would undo whatever made the edit stale.
    api<ContentEdit>(`/api/edits/${encodeURIComponent(from)}`)
      .then(async (e) => {
        const head = await headVersions((e.ops ?? []).flatMap((op) => (op.op === 'put' ? [op.path] : [])), loadFile)
        setOld(e); setTitle(e.title); setOrig(head); setFiles(head); setOpen(Object.keys(head)[0] ?? '')
      })
      .catch((e: Error) => setErr(e.message))
  }, [from]) // eslint-disable-line react-hooks/exhaustive-deps

  const openFile = async (p: string) => {
    setOpen(p)
    if (p in files) return
    try {
      const content = await loadFile(p)
      setOrig((o) => ({ ...o, [p]: content }))
      setFiles((s) => ({ ...s, [p]: content }))
    } catch (e) { setErr((e as Error).message) }
  }
  const changed = changedFiles(orig, files)
  const problem = Object.keys(changed).length ? editProblem(changed) : undefined

  const submit = async () => {
    setErr('')
    const p = editProblem(changed)
    if (p) return setErr(p)
    if (!title.trim()) return setErr('Give the edit a title.')
    if (!list) return setErr('The file list has not loaded yet.')
    setBusy(true)
    try {
      const e = await api<ContentEdit>('/api/edits', { method: 'POST', json: { training, base_sha: list.head_sha, title: title.trim(), files: changed } })
      nav(`/edits/${e.id}`)
    } catch (e) { setErr((e as Error).message) } finally { setBusy(false) }
  }

  if (!list && !err) return <section className="page"><Loader label="Loading files…" /></section>
  return (
    <section className="page">
      <h1>Edit {training}</h1>
      {err && <p role="alert" className="error">{err}</p>}
      <p role="status" className="muted">{problem ?? ''}</p>
      {old && (
        <div role="note">
          <p>Redoing <strong>{old.title}</strong>. The files below hold the current version; the old edit no longer applied, so re-apply
            your changes here. Its diff, for reference:</p>
          <DiffView diff={old.diff ?? ''} />
        </div>
      )}
      <div className="editor">
        <div>
          <ul>
            {(list?.files ?? []).filter((f) => f.editable).map((f) => (
              <li key={f.path}>
                <button className={open === f.path ? '' : 'ghost'} aria-current={open === f.path ? 'true' : undefined} onClick={() => openFile(f.path)}>{f.path}</button>
                {f.path in changed && <span aria-hidden="true"> *<span className="sr-only"> (changed)</span></span>}
              </li>
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
        <label>Title <input value={title} onChange={(e) => byteLen(e.target.value) <= 200 && setTitle(e.target.value)} /></label>{' '}
        <button disabled={busy || !list} onClick={submit}>Submit for review</button>
      </p>
    </section>
  )
}
