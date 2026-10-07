import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router'
import { api, type ApiError } from '../../api'
import type { ContentEdit, DraftInfo, FileEntry } from '../../types'
import { ErrorBox } from '../../components/ErrorBox'
import { Loader } from '../../components/Loader'
import { opsProblem } from '../../lib/editLimits'
import { CodeEditor } from './CodeEditor'
import { Explorer } from './Explorer'
import { Preview } from './Preview'
import { setupYaml } from './monaco'
import { afterSave, canAutosave, saveLabel, type SaveState } from './autosave'
import { changeList, currentPaths, deletePath, emptyOps, fromOps, origin, putText, renamePath, toOps, type DraftOps } from './model'

const AUTOSAVE_MS = 2000

export default function Ide() {
  const id = Number(useParams().id)
  const nav = useNavigate()
  const [draft, setDraft] = useState<DraftInfo>()
  const [files, setFiles] = useState<FileEntry[]>([])
  const [work, setWork] = useState<DraftOps>(emptyOps)
  const [baseText, setBaseText] = useState<Record<string, string>>({})
  const [tabs, setTabs] = useState<string[]>([])
  const [active, setActive] = useState('')
  const [title, setTitle] = useState('')
  const [save, setSave] = useState<SaveState>(() => ({ kind: 'saved', at: Date.now() }))
  const [msg, setMsg] = useState('')
  const [fatal, setFatal] = useState<ApiError>()
  const [now, setNow] = useState(() => Date.now())
  const explorerRef = useRef<HTMLDivElement>(null)

  const load = useCallback(async () => {
    const [d, f] = await Promise.all([api<DraftInfo>(`/api/authoring/drafts/${id}`), api<FileEntry[]>(`/api/authoring/drafts/${id}/files`)])
    setupYaml(await api<Record<string, object>>(`/api/authoring/schema?training=${encodeURIComponent(d.training)}`))
    setDraft(d); setFiles(f); setWork(fromOps(d.ops)); setTitle(d.title); setBaseText({})
    setSave({ kind: 'saved', at: Date.now() })
  }, [id])
  useEffect(() => { load().catch(setFatal) }, [load])
  useEffect(() => { const t = setInterval(() => setNow(Date.now()), 5000); return () => clearInterval(t) }, [])

  const base = useMemo(() => files.filter((f) => f.editable).map((f) => f.path), [files])
  const greyed = useMemo(() => files.filter((f) => !f.editable).map((f) => ({ path: f.path, reason: f.reason ?? '' })), [files])
  const paths = useMemo(() => currentPaths(base, work), [base, work])
  const changes = useMemo(() => changeList(base, work), [base, work])
  const ops = useMemo(() => toOps(work), [work])
  const problem = opsProblem(ops)
  const originalOf = (p: string) => { const o = origin(base, work, p); return o === undefined ? undefined : baseText[o] }
  const textOf = (p: string) => work.puts[p] ?? originalOf(p) ?? ''

  const loadBase = async (p: string | undefined) => {
    if (!p || p in baseText) return
    const { content } = await api<{ content: string }>(`/api/authoring/drafts/${id}/file?path=${encodeURIComponent(p)}`)
    setBaseText((b) => ({ ...b, [p]: content }))
  }
  const change = (next: DraftOps) => { setWork(next); setSave((s) => (canAutosave(s) ? { kind: 'dirty' } : s)) }
  const open = async (p: string) => {
    try { await loadBase(origin(base, work, p)) } catch (e) { return setMsg((e as Error).message) }
    setTabs((t) => (t.includes(p) ? t : [...t, p]))
    setActive(p)
  }
  const edit = (p: string, text: string) => { if (text !== textOf(p)) change(putText(work, p, text, originalOf(p))) }
  const create = (p: string) => {
    if (paths.includes(p)) return setMsg(`${p} already exists.`)
    change(putText(work, p, '', undefined)); setTabs((t) => [...t, p]); setActive(p)
  }
  const rename = async (from: string, to: string) => {
    try { await loadBase(origin(base, work, from)); change(renamePath(base, work, from, to)) } catch (e) { return setMsg((e as Error).message) }
    setTabs((t) => t.map((x) => (x === from ? to : x)))
    if (active === from) setActive(to)
  }
  const remove = (p: string) => {
    change(deletePath(base, work, p)); setTabs((t) => t.filter((x) => x !== p))
    if (active === p) setActive('')
  }
  const close = (p: string) => { setTabs((t) => t.filter((x) => x !== p)); if (active === p) setActive(tabs.find((x) => x !== p) ?? '') }
  const leave = () => explorerRef.current?.querySelector<HTMLButtonElement>('button[aria-current="true"], button')?.focus()

  // saveNow saves the draft and returns it as saved. base_sha only moves on a rebase, never here.
  const saveNow = useCallback(async (): Promise<DraftInfo | undefined> => {
    if (!draft) return undefined
    if (problem) { setSave({ kind: 'blocked', reason: problem }); return undefined }
    setSave({ kind: 'saving' })
    try {
      const d = await api<DraftInfo>(`/api/authoring/drafts/${id}`, { method: 'PUT', json: { title, base_sha: draft.base_sha, ops, updated_at: draft.updated_at } })
      setDraft(d)
      setSave((s) => (s.kind === 'dirty' ? s : afterSave(undefined, Date.now()))) // typed while saving: still dirty
      return d
    } catch (e) {
      setSave(afterSave(e as ApiError, Date.now()))
      return undefined
    }
  }, [draft, id, title, ops, problem])
  useEffect(() => { // autosave about 2 s after the last change; never once another tab owns the draft
    if (save.kind !== 'dirty') return
    const t = setTimeout(saveNow, AUTOSAVE_MS)
    return () => clearTimeout(t)
  }, [save, saveNow])

  const submit = async () => {
    setMsg('')
    if (!title.trim()) return setMsg("Give the draft a title first: it becomes the edit's title.")
    const d = save.kind === 'saved' ? draft : await saveNow()
    if (!d) return
    try {
      const e = await api<ContentEdit>(`/api/authoring/drafts/${id}/submit`, { method: 'POST', json: { updated_at: d.updated_at } })
      nav(`/edits/${e.id}`)
    } catch (e) { setMsg((e as Error).message) }
  }

  if (fatal) return <ErrorBox error={fatal} />
  if (!draft) return <Loader label="Heating the editor…" />
  const readOnly = draft.state === 'in_review' || save.kind === 'conflict'
  return (
    <section className="ide" aria-label={`Editing ${draft.training}`}>
      <header className="ide-bar">
        <Link to="/edits">All edits</Link>
        <strong>{draft.training}</strong>
        <label>Title <input value={title} maxLength={200} disabled={readOnly} onChange={(e) => { setTitle(e.target.value); setSave((s) => (canAutosave(s) ? { kind: 'dirty' } : s)) }} /></label>
        {draft.base_sha !== draft.head_sha && <span role="note">Newer content is on the branch.</span>}
      </header>
      {draft.state === 'in_review' && <p role="note">This draft is in review. <Link to={`/edits/${draft.edit_id}`}>Open the edit</Link> and withdraw it to keep working here.</p>}
      {save.kind === 'conflict' && <p role="alert" className="error">{save.reason} <button onClick={() => load().catch(setFatal)}>Reload</button></p>}
      <p role="alert" className="error">{msg}</p>
      <div className="ide-main">
        <div ref={explorerRef}>
          <Explorer paths={paths} greyed={greyed} changed={new Set(changes.map((c) => c.path))} active={active} readOnly={readOnly}
            onOpen={open} onNew={create} onRename={rename} onDelete={remove} />
        </div>
        <div className="ide-editor">
          <div className="ide-tabs" role="toolbar" aria-label="Open files">
            {tabs.map((t) => (
              <span key={t}>
                <button className={t === active ? '' : 'ghost'} aria-current={t === active ? 'true' : undefined} onClick={() => setActive(t)} title={t}>
                  {t.slice(t.lastIndexOf('/') + 1)}{t in work.puts && <span aria-label=" (unsaved changes)"> •</span>}
                </button>
                <button className="ghost small" aria-label={`Close ${t}`} onClick={() => close(t)}>×</button>
              </span>
            ))}
          </div>
          {active
            ? <CodeEditor draftId={id} path={active} text={textOf(active)} readOnly={readOnly} markers={[]} onChange={edit} onLeave={leave} onGoToFile={() => {}} />
            : <p className="muted">Open a file from the explorer.</p>}
        </div>
        {active && <div className="ide-preview" data-testid="edit-preview"><Preview path={active} text={textOf(active)} read={(p) => (paths.includes(p) ? textOf(p) : undefined)} /></div>}
      </div>
      <footer className="ide-status" role="status" aria-live="polite">
        <span>{saveLabel(save, now)}</span>
        <span>base {draft.base_sha.slice(0, 7)}</span>
        <button disabled={readOnly} onClick={submit}>Submit for review</button>
      </footer>
    </section>
  )
}
