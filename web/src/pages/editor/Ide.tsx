import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router'
import { api, type ApiError } from '../../api'
import type { Conflict, ContentEdit, DraftInfo, FileEntry, Problem } from '../../types'
import { ErrorBox } from '../../components/ErrorBox'
import { Loader } from '../../components/Loader'
import { opsProblem } from '../../lib/editLimits'
import { CodeEditor } from './CodeEditor'
import { Explorer } from './Explorer'
import { Preview } from './Preview'
import { parseLab } from './previewModel'
import { ProblemsPanel } from './ProblemsPanel'
import { ChangesPanel } from './ChangesPanel'
import { GoToFile } from './GoToFile'
import { RebasePanel } from './RebasePanel'
import { resolveOps, withBase, type BaseTexts, type Choice } from './rebase'
import { unifiedDiff } from './diff'
import { setupYaml } from './monaco'
import { afterSave, canAutosave, saveLabel, serialSaves, type SaveState } from './autosave'
import { changeList, currentPaths, deletePath, emptyOps, fileText, fromOps, origin, putText, renamePath, toOps, type Change, type DraftOps } from './model'

const AUTOSAVE_MS = 2000
const VALIDATE_MS = 1000

export default function Ide() {
  const id = Number(useParams().id)
  const nav = useNavigate()
  const [draft, setDraft] = useState<DraftInfo>()
  const [files, setFiles] = useState<FileEntry[]>([])
  const [work, setWork] = useState<DraftOps>(emptyOps)
  const [bt, setBt] = useState<BaseTexts>({ sha: '', text: {} }) // tagged with their base: a fetch for an older base is dropped
  const baseText = bt.text
  const [conflicts, setConflicts] = useState<Conflict[]>()
  const [tabs, setTabs] = useState<string[]>([])
  const [active, setActive] = useState('')
  const [title, setTitle] = useState('')
  const [save, setSave] = useState<SaveState>(() => ({ kind: 'saved', at: Date.now() }))
  const [msg, setMsg] = useState('')
  const [fatal, setFatal] = useState<ApiError>()
  const [now, setNow] = useState(() => Date.now())
  const [problems, setProblems] = useState<Problem[]>([])
  const [panel, setPanel] = useState<'explorer' | 'problems' | 'changes'>('explorer')
  const [reveal, setReveal] = useState<{ line: number; n: number }>()
  const [goto, setGoto] = useState(false)
  const [view, setView] = useState<'files' | 'editor' | 'preview'>('editor') // below tablet width only
  const explorerRef = useRef<HTMLDivElement>(null)
  const saver = useMemo(() => serialSaves<DraftInfo>(), [])
  const asked = useRef(new Set<string>()) // originals requested for the Changes panel (once each; a failure is not retried)
  const vseq = useRef(0) // latest validate request
  const pending = useRef(0) // saves queued or in flight

  const load = useCallback(async () => {
    const [d, f] = await Promise.all([api<DraftInfo>(`/api/authoring/drafts/${id}`), api<FileEntry[]>(`/api/authoring/drafts/${id}/files`)])
    setupYaml(await api<Record<string, object>>(`/api/authoring/schema?training=${encodeURIComponent(d.training)}`))
    const kept = currentPaths(f.filter((x) => x.editable).map((x) => x.path), fromOps(d.ops))
    saver.set(d)
    setDraft(d); setFiles(f); setWork(fromOps(d.ops)); setTitle(d.title); setBt({ sha: d.base_sha, text: {} }); asked.current.clear() // open tabs refetch their base text
    setTabs((t) => t.filter((p) => kept.includes(p))); setActive((a) => (kept.includes(a) ? a : ''))
    setSave({ kind: 'saved', at: Date.now() })
  }, [id, saver])
  useEffect(() => { load().catch(setFatal) }, [load])
  useEffect(() => { const t = setInterval(() => setNow(Date.now()), 5000); return () => clearInterval(t) }, [])

  const base = useMemo(() => files.filter((f) => f.editable).map((f) => f.path), [files])
  const greyed = useMemo(() => files.filter((f) => !f.editable).map((f) => ({ path: f.path, reason: f.reason ?? '' })), [files])
  const paths = useMemo(() => currentPaths(base, work), [base, work])
  const changes = useMemo(() => changeList(base, work), [base, work])
  const ops = useMemo(() => toOps(work), [work])
  const problem = opsProblem(ops)
  const originalOf = (p: string) => { const o = origin(base, work, p); return o === undefined ? undefined : baseText[o] }
  const textOf = (p: string) => fileText(base, work, baseText, p) // undefined until its base text is loaded

  const loadBase = async (p: string | undefined) => {
    if (!p || p in baseText) return
    const sha = bt.sha
    const { content } = await api<{ content: string }>(`/api/authoring/drafts/${id}/file?path=${encodeURIComponent(p)}`)
    setBt((b) => withBase(b, sha, p, content))
  }
  useEffect(() => { // the open file's base text, e.g. after a reload cleared them
    const o = active ? origin(base, work, active) : undefined
    if (o === undefined || o in bt.text) return
    const sha = bt.sha
    api<{ content: string }>(`/api/authoring/drafts/${id}/file?path=${encodeURIComponent(o)}`)
      .then(({ content }) => setBt((b) => withBase(b, sha, o, content)))
      .catch((e: Error) => setMsg(e.message))
  }, [active, base, work, bt, id])
  useEffect(() => { // originals of changed files, for their diffs: each fetched once, a failure reported not retried
    if (panel !== 'changes') return
    for (const c of changes) {
      const o = c.kind === 'added' ? undefined : (c.from ?? c.path)
      if (o === undefined || o in bt.text || asked.current.has(o)) continue
      asked.current.add(o)
      const sha = bt.sha
      api<{ content: string }>(`/api/authoring/drafts/${id}/file?path=${encodeURIComponent(o)}`)
        .then(({ content }) => setBt((b) => withBase(b, sha, o, content)))
        .catch(() => setMsg(`Couldn't load the original of ${o}.`))
    }
  }, [panel, changes, bt, id])
  useEffect(() => { // a lab preview shows its task instructions: load them (existing files only; a failure just leaves the path shown)
    if (!active.endsWith('/lab.yaml')) return
    const labDir = active.slice(0, active.lastIndexOf('/') + 1)
    for (const t of parseLab(textOf(active) ?? '').lab?.tasks ?? []) {
      const p = labDir + t.instructions
      if (paths.includes(p)) loadBase(origin(base, work, p)).catch(() => {})
    }
  }, [active, work, bt]) // eslint-disable-line react-hooks/exhaustive-deps
  const change = (next: DraftOps) => { setWork(next); setSave((s) => (canAutosave(s) ? { kind: 'dirty' } : s)) }
  const open = async (p: string) => {
    try { await loadBase(origin(base, work, p)) } catch (e) { return setMsg((e as Error).message) }
    setTabs((t) => (t.includes(p) ? t : [...t, p]))
    setActive(p)
  }
  const edit = (p: string, text: string) => {
    const cur = textOf(p)
    if (cur !== undefined && text !== cur) change(putText(work, p, text, originalOf(p)))
  }
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

  // saveNow saves the draft and returns it as saved. Saves queue behind the one in flight (serialSaves), each with the
  // updated_at the last one returned. base_sha only moves on a rebase, never here.
  const saveNow = useCallback(async (): Promise<DraftInfo | undefined> => {
    if (!saver.get()) return undefined
    if (problem) { setSave({ kind: 'blocked', reason: problem }); return undefined }
    setSave({ kind: 'saving' })
    pending.current++
    try {
      const d = await saver.run((cur) => api<DraftInfo>(`/api/authoring/drafts/${id}`, { method: 'PUT', json: { title, base_sha: cur.base_sha, ops, updated_at: cur.updated_at } }))
      setDraft(d)
      const last = pending.current === 1
      setSave((s) => (s.kind === 'dirty' || !last ? s : afterSave(undefined, Date.now()))) // typed or queued another: not saved yet
      return d
    } catch (e) {
      setSave(afterSave(e as ApiError, Date.now()))
      return undefined
    } finally {
      pending.current--
    }
  }, [saver, id, title, ops, problem])
  useEffect(() => { // autosave about 2 s after the last change; never once another tab owns the draft
    if (save.kind !== 'dirty') return
    const t = setTimeout(saveNow, AUTOSAVE_MS)
    return () => clearTimeout(t)
  }, [save, saveNow])

  useEffect(() => { // closing the tab with work the server doesn't have yet asks first
    if (save.kind === 'saved' || save.kind === 'conflict') return
    const warn = (e: BeforeUnloadEvent) => e.preventDefault()
    window.addEventListener('beforeunload', warn)
    return () => window.removeEventListener('beforeunload', warn)
  }, [save.kind])

  useEffect(() => { // problems about a second after typing stops; a 409 means a check is still running, and the next change re-runs it
    if (!draft || problem) return
    const seq = ++vseq.current
    const run = (retry: boolean) => api<{ problems: Problem[] }>('/api/authoring/validate', { method: 'POST', json: { training: draft.training, base_sha: draft.base_sha, ops } })
      .then((r) => { if (seq === vseq.current) setProblems(r.problems) }) // an older answer never overwrites a newer one
      .catch((e: ApiError) => {
        if (seq !== vseq.current) return
        if (e.status === 409 && retry) retries = setTimeout(() => run(false), 1500) // another check is running: once more
        else if (e.status !== 409) setMsg(e.message)
      })
    let retries: ReturnType<typeof setTimeout> | undefined
    const t = setTimeout(() => run(true), VALIDATE_MS)
    return () => { clearTimeout(t); clearTimeout(retries) }
  }, [draft?.training, draft?.base_sha, ops, problem]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => { // Ctrl/Cmd+P outside the editor too (Monaco handles it inside)
    const k = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && !e.shiftKey && e.key.toLowerCase() === 'p') { e.preventDefault(); setGoto(true) }
    }
    window.addEventListener('keydown', k)
    return () => window.removeEventListener('keydown', k)
  }, [])

  const jump = async (p: Problem) => { await open(p.file); setView('editor'); setReveal({ line: p.line, n: Date.now() }) }
  const diffOf = (c: Change): string | undefined => {
    const o = c.kind === 'added' ? undefined : (c.from ?? c.path)
    if (o !== undefined && !(o in baseText)) return undefined
    const before = o === undefined ? '' : baseText[o]
    return unifiedDiff(o, c.kind === 'deleted' ? undefined : c.path, before, c.kind === 'deleted' ? '' : textOf(c.path) ?? '')
  }

  const submit = async () => {
    setMsg('')
    if (!title.trim()) return setMsg("Give the draft a title first: it becomes the edit's title.")
    if (save.kind !== 'saved' && !(await saveNow())) return
    try {
      let edit: ContentEdit | undefined
      await saver.run(async (d) => { // after any save still in flight, with its updated_at
        edit = await api<ContentEdit>(`/api/authoring/drafts/${id}/submit`, { method: 'POST', json: { updated_at: d.updated_at } })
        return d
      })
      nav(`/edits/${edit!.id}`)
    } catch (e) { setMsg((e as Error).message) }
  }

  // rebase moves the draft onto the newest content. Like every save it runs in the serialSaves queue, after any save in
  // flight. With no conflicts the server moves base_sha itself; otherwise the author resolves each one first.
  const rebase = async () => {
    setMsg('')
    if (save.kind !== 'saved' && !(await saveNow())) return
    try {
      let found: Conflict[] = []
      await saver.run(async () => {
        const r = await api<{ draft: DraftInfo; conflicts: Conflict[] }>(`/api/authoring/drafts/${id}/rebase`, { method: 'POST', json: {} })
        found = r.conflicts
        return r.draft
      })
      if (found.length === 0) { await load(); setMsg('Rebased onto the newest content.') } else setConflicts(found)
    } catch (e) { setMsg((e as Error).message) }
  }
  // resolved saves the author's choices with base_sha at the head the conflicts were found against: if the training
  // moved again since, the server refuses it and the author rebases once more.
  const resolved = async (choices: Choice[]) => {
    if (!conflicts) return
    try {
      await saver.run((cur) => api<DraftInfo>(`/api/authoring/drafts/${id}`, { method: 'PUT', json: { title, base_sha: cur.head_sha, ops: resolveOps(cur.ops, conflicts, choices), updated_at: cur.updated_at } }))
      setConflicts(undefined)
      await load() // new base: new file list, base texts reloaded on demand
      setMsg('Rebased onto the newest content.')
    } catch (e) { setConflicts(undefined); setMsg((e as Error).message) }
  }
  const discard = async () => {
    if (!window.confirm('Discard this draft? Your changes in it are lost.')) return
    try {
      await saver.run(async (d) => { await api(`/api/authoring/drafts/${id}`, { method: 'DELETE' }); return d }) // after any save in flight
      nav('/edits')
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
        {draft.base_sha !== draft.head_sha && draft.state !== 'in_review' && <button disabled={!!conflicts || save.kind === 'conflict'} onClick={rebase}>Newer content: rebase draft</button>}
      </header>
      {draft.state === 'in_review' && <p role="note">This draft is in review. <Link to={`/edits/${draft.edit_id}`}>Open the edit</Link> and withdraw it to keep working here.</p>}
      {save.kind === 'conflict' && <p role="alert" className="error">{save.reason} <button onClick={() => load().catch(setFatal)}>Reload</button></p>}
      <p role="alert" className="error">{msg}</p>
      <div role="tablist" aria-label="Editor views" className="ide-views">
        {(['files', 'editor', 'preview'] as const).map((v) => <button key={v} role="tab" aria-selected={view === v} onClick={() => setView(v)}>{v[0].toUpperCase() + v.slice(1)}</button>)}
      </div>
      {conflicts ? <RebasePanel conflicts={conflicts} onDone={resolved} onCancel={() => setConflicts(undefined)} /> : (
      <div className="ide-main" data-view={view}>
        <div ref={explorerRef} className="ide-side">
          <div role="toolbar" aria-label="Panels" className="ide-activity">
            <button aria-pressed={panel === 'explorer'} onClick={() => setPanel('explorer')}>Explorer</button>
            <button aria-pressed={panel === 'problems'} onClick={() => setPanel('problems')}>Problems ({problems.length})</button>
            <button aria-pressed={panel === 'changes'} onClick={() => setPanel('changes')}>Changes ({changes.length})</button>
          </div>
          {panel === 'explorer' && <Explorer paths={paths} greyed={greyed} changed={new Set(changes.map((c) => c.path))} active={active} readOnly={readOnly}
            onOpen={(p) => { open(p); setView('editor') }} onNew={create} onRename={rename} onDelete={remove} />}
          {panel === 'problems' && <ProblemsPanel problems={problems} known={(f) => paths.includes(f)} onJump={jump} />}
          {panel === 'changes' && <ChangesPanel changes={changes} diffOf={diffOf} onOpen={(p) => { open(p); setView('editor') }} />}
        </div>
        <div className="ide-editor">
          <div className="ide-tabs" role="toolbar" aria-label="Open files">
            {tabs.map((t) => (
              <span key={t}>
                <button className={t === active ? '' : 'ghost'} aria-current={t === active ? 'true' : undefined} onClick={() => setActive(t)} title={t}>
                  {t.slice(t.lastIndexOf('/') + 1)}{t in work.puts && <span aria-label=" (changed)"> •</span>}
                </button>
                <button className="ghost small" aria-label={`Close ${t}`} onClick={() => close(t)}>×</button>
              </span>
            ))}
          </div>
          {active && textOf(active) === undefined && <Loader label="Opening the file…" />}
          {active && textOf(active) !== undefined
            ? <CodeEditor draftId={id} path={active} text={textOf(active)!} readOnly={readOnly} markers={problems.filter((p) => p.file === active).map((p) => ({ line: p.line, message: p.msg }))} reveal={reveal} onChange={edit} onLeave={leave} onGoToFile={() => setGoto(true)} />
            : !active && <p className="muted">Open a file from the explorer.</p>}
        </div>
        {active && <div className="ide-preview" data-testid="edit-preview"><Preview path={active} text={textOf(active) ?? ''} read={textOf} /></div>}
      </div>)}
      <footer className="ide-status" role="status" aria-live="polite">
        <span>{saveLabel(save, now)}</span>
        <span>{problems.length} problems</span>
        <span>base {draft.base_sha.slice(0, 7)}</span>
        <button disabled={readOnly || !!conflicts} onClick={submit}>Submit for review</button>
        <button className="ghost" onClick={discard}>Discard draft</button>
      </footer>
      {goto && <GoToFile paths={paths} onPick={(p) => { setGoto(false); open(p); setView('editor') }} onClose={() => setGoto(false)} />}
    </section>
  )
}
