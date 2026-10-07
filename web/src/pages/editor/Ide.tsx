import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router'
import { api, type ApiError } from '../../api'
import type { BlockInfo, Conflict, ContentEdit, DraftInfo, EditOp, FileEntry, Problem } from '../../types'
import { ErrorBox } from '../../components/ErrorBox'
import { Loader } from '../../components/Loader'
import { opsProblem } from '../../lib/editLimits'
import { CodeEditor } from './CodeEditor'
import { Explorer } from './Explorer'
import { BlocksPanel } from './BlocksPanel'
import { Preview } from './Preview'
import { parseLab } from './previewModel'
import { ProblemsPanel } from './ProblemsPanel'
import { ChangesPanel } from './ChangesPanel'
import { GoToFile } from './GoToFile'
import { RebasePanel } from './RebasePanel'
import { resolveOps, withBase, type BaseTexts, type Choice } from './rebase'
import { unifiedDiff } from './diff'
import { setupYaml } from './monaco'
import { Icon, type IconName } from './icons'
import { bounds, DEFAULT_LAYOUT, fit, LAYOUT_KEY, parseLayout, resize, type Layout } from './layout'
import { afterSave, canAutosave, onLeave, saveLabel, serialSaves, type SaveState } from './autosave'
import { applyInsert, changeList, currentPaths, deletePath, emptyOps, fileText, fromOps, origin, putText, renamePath, toOps, type Change, type DraftOps } from './model'

const AUTOSAVE_MS = 2000
const ACTIVITY = 44 // px: the activity bar, left of the side panel
const SPLIT = 1 // px: a splitter's column (its grab area is wider)
const VALIDATE_MS = 1000

export default function Ide() {
  const id = Number(useParams().id)
  const nav = useNavigate()
  const [draft, setDraft] = useState<DraftInfo>()
  const [files, setFiles] = useState<FileEntry[]>([])
  const [work, setWork] = useState<DraftOps>(emptyOps)
  const [bt, setBt] = useState<BaseTexts>({ sha: '', text: {} }) // tagged with their base: a fetch for an older base is dropped
  const baseText = bt.text
  const [conflicts, setConflicts] = useState<{ list: Conflict[]; head: string }>() // head: what the conflicts were found against
  const resolving = useRef(false) // set while conflicts are open: nothing autosaves the pre-resolution ops
  const [tabs, setTabs] = useState<string[]>([])
  const [active, setActive] = useState('')
  const [title, setTitle] = useState('')
  const [save, setSave] = useState<SaveState>(() => ({ kind: 'saved', at: Date.now() }))
  const [msg, setMsg] = useState('')
  const [fatal, setFatal] = useState<ApiError>()
  const [now, setNow] = useState(() => Date.now())
  const [problems, setProblems] = useState<Problem[]>([])
  const [panel, setPanel] = useState<'explorer' | 'problems' | 'changes' | 'blocks'>('explorer')
  const [reveal, setReveal] = useState<{ line: number; lines?: number; n: number }>()
  const [catalog, setCatalog] = useState<{ groups: string[]; blocks: BlockInfo[] }>()
  const [preselect, setPreselect] = useState<string>() // the block Blocks opens on, e.g. New module
  const [goto, setGoto] = useState(false)
  const [view, setView] = useState<'files' | 'editor' | 'preview'>('editor') // below tablet width only
  const explorerRef = useRef<HTMLDivElement>(null)
  const saver = useMemo(() => serialSaves<DraftInfo>(), [])
  const asked = useRef(new Set<string>()) // originals requested for the Changes panel (once each; a failure is not retried)
  const vseq = useRef(0) // latest validate request
  const validateNow = useRef(false) // the next change is checked at once, not after the pause (an insert)
  const latest = useRef<DraftOps>(emptyOps()) // the draft as last set, ahead of the render: an insert is checked against it
  const frozen = useRef(false) // read-only as of the last render: an insert that lands then is dropped
  const pending = useRef(0) // saves queued or in flight
  const [rebasing, setRebasing] = useState(false) // a rebase request is running: read-only, nothing autosaves
  const flush = useRef(() => {}) // saves work the server doesn't have yet when the editor unmounts
  const [layout, setLayout] = useState<Layout>(() => { try { return parseLayout(localStorage.getItem(LAYOUT_KEY)) } catch { return DEFAULT_LAYOUT } })
  useEffect(() => { try { localStorage.setItem(LAYOUT_KEY, JSON.stringify(layout)) } catch { /* storage blocked: sizes last this visit */ } }, [layout])
  const [width, setWidth] = useState(() => window.innerWidth) // of .ide-main, measured once it renders
  const measure = useCallback((el: HTMLDivElement | null) => {
    if (!el) return
    setWidth(el.clientWidth)
    const ro = new ResizeObserver(() => setWidth(el.clientWidth))
    ro.observe(el)
    return () => ro.disconnect()
  }, [])

  const load = useCallback(async () => {
    const [d, f] = await Promise.all([api<DraftInfo>(`/api/authoring/drafts/${id}`), api<FileEntry[]>(`/api/authoring/drafts/${id}/files`)])
    const q = `?training=${encodeURIComponent(d.training)}`
    const [schema, cat] = await Promise.all([api<Record<string, object>>(`/api/authoring/schema${q}`), api<{ groups: string[]; blocks: BlockInfo[] }>(`/api/authoring/blocks${q}`)])
    setupYaml(schema); setCatalog(cat)
    const kept = currentPaths(f.filter((x) => x.editable).map((x) => x.path), fromOps(d.ops))
    saver.set(d)
    latest.current = fromOps(d.ops)
    setDraft(d); setFiles(f); setWork(latest.current); setTitle(d.title); setBt({ sha: d.base_sha, text: {} }); asked.current.clear() // open tabs refetch their base text
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

  // loadBase loads p's base text and returns it, so a caller past an await doesn't read baseText from a stale render.
  const loadBase = async (p: string | undefined): Promise<string | undefined> => {
    if (!p) return undefined
    if (p in baseText) return baseText[p]
    const sha = bt.sha
    const { content } = await api<{ content: string }>(`/api/authoring/drafts/${id}/file?path=${encodeURIComponent(p)}`)
    setBt((b) => withBase(b, sha, p, content))
    return content
  }
  // need loads a file the Blocks panel reads (a module's lab.yaml, for its task picker)
  const need = useCallback((p: string) => { loadBase(origin(base, work, p)).catch(() => {}) }, [base, work, bt, id]) // eslint-disable-line react-hooks/exhaustive-deps
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
  const change = (next: DraftOps) => { latest.current = next; setWork(next); setSave((s) => (canAutosave(s, resolving.current) ? { kind: 'dirty' } : s)) }
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
  // insert has the server render a block against the draft and applies the files it returns. They replace whole files,
  // so it is refused when one of them changed while the request ran (applyInsert), or the draft moved or became read-only.
  // Nothing is saved here: the change autosaves through the queue like typing.
  const insert = async (block: BlockInfo, values: Record<string, string>) => {
    const sent = latest.current
    const sentBase = draft!.base_sha
    const r = await api<{ ops: EditOp[]; open: string; line: number; lines: number }>('/api/authoring/insert',
      { method: 'POST', json: { training: draft!.training, base_sha: sentBase, ops: toOps(sent), block: block.id, values } })
    const puts = r.ops.flatMap((o) => (o.op === 'put' ? [o] : []))
    const originals: Record<string, string | undefined> = {} // by base path: a file edited back to its original leaves the draft
    for (const o of puts) {
      const b = origin(base, sent, o.path)
      if (b !== undefined) originals[b] = await loadBase(b).catch(() => undefined)
    }
    if (frozen.current) throw new Error('The draft became read-only while the block was being added; nothing was added.')
    if (resolving.current || saver.get()?.base_sha !== sentBase) throw new Error('The draft moved to newer content meanwhile: add the block again.')
    const res = applyInsert(base, sent, latest.current, puts, originals)
    if ('changed' in res) throw new Error(`${res.changed} changed while the block was being added; add it again.`)
    change(res.next)
    validateNow.current = true // problems at once, not after the pause
    await open(r.open)
    setView('editor')
    setReveal({ line: r.line, lines: r.lines, n: Date.now() })
    setPreselect(undefined)
    setPanel('explorer') // the new files are in view; Blocks reopens on its list
  }
  const close = (p: string) => { setTabs((t) => t.filter((x) => x !== p)); if (active === p) setActive(tabs.find((x) => x !== p) ?? '') }
  const leave = () => (explorerRef.current?.querySelector<HTMLElement>('[aria-current="true"]') ?? explorerRef.current?.querySelector<HTMLElement>('button'))?.focus()

  // saveNow saves the draft and returns it as saved. Saves queue behind the one in flight (serialSaves), each with the
  // updated_at the last one returned. base_sha only moves on a rebase, never here.
  const saveNow = useCallback(async (): Promise<DraftInfo | undefined> => {
    if (!saver.get() || resolving.current) return undefined
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
    if (save.kind !== 'dirty' || conflicts) return // opening the conflict panel cancels a pending autosave
    const t = setTimeout(saveNow, AUTOSAVE_MS)
    return () => clearTimeout(t)
  }, [save, saveNow, conflicts])

  useEffect(() => { // closing the tab with work the server doesn't have yet asks first
    if (save.kind === 'saved' || save.kind === 'conflict') return
    const warn = (e: BeforeUnloadEvent) => e.preventDefault()
    window.addEventListener('beforeunload', warn)
    return () => window.removeEventListener('beforeunload', warn)
  }, [save.kind])

  useEffect(() => { // problems about a second after typing stops; a 409 means a check is still running, and the next change re-runs it
    const delay = validateNow.current ? 0 : VALIDATE_MS
    validateNow.current = false
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
    const t = setTimeout(() => run(true), delay)
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
  // The editor is read-only from the start, and nothing autosaves while the request runs: typing then would be replaced
  // by the rebased draft, or would change a put so the author's conflict choice no longer applies to it.
  const rebase = async () => {
    setMsg('')
    setRebasing(true)
    try {
      if (save.kind !== 'saved' && !(await saveNow())) return
      resolving.current = true
      let found: Conflict[] = []
      const d = await saver.run(async () => {
        const r = await api<{ draft: DraftInfo; conflicts: Conflict[] }>(`/api/authoring/drafts/${id}/rebase`, { method: 'POST', json: {} })
        found = r.conflicts
        return r.draft
      })
      if (found.length === 0) { await load(); resolving.current = false; setMsg('Rebased onto the newest content.') } else setConflicts({ list: found, head: d.head_sha })
    } catch (e) { resolving.current = false; setMsg((e as Error).message) } finally { setRebasing(false) }
  }
  // resolved saves the author's choices with base_sha at the head the conflicts were found against (never a newer one:
  // its changes were not checked); if the training moved again since, the server refuses it and the author rebases again.
  const resolved = async (choices: Choice[]) => {
    if (!conflicts) return
    const done = () => { resolving.current = false; setConflicts(undefined) }
    try {
      await saver.run((cur) => api<DraftInfo>(`/api/authoring/drafts/${id}`, { method: 'PUT', json: { title, base_sha: conflicts.head, ops: resolveOps(cur.ops, conflicts.list, choices), updated_at: cur.updated_at } }))
    } catch (e) { done(); return setMsg((e as Error).message) }
    try {
      await load() // new base: new file list, base texts reloaded on demand; the panel stays up (and autosave off) until then
      setMsg('Rebased onto the newest content.')
    } catch {
      setSave({ kind: 'conflict', reason: "Rebased, but the editor couldn't load the result. Reload before you go on." }) // stops autosave
    }
    done()
  }
  const discard = async () => {
    if (!window.confirm('Discard this draft? Your changes in it are lost.')) return
    try {
      await saver.run(async (d) => { await api(`/api/authoring/drafts/${id}`, { method: 'DELETE' }); return d }) // after any save in flight
      nav('/edits')
    } catch (e) { setMsg((e as Error).message) }
  }

  const readOnly = draft?.state === 'in_review' || save.kind === 'conflict' || rebasing
  useEffect(() => { frozen.current = readOnly }, [readOnly])

  // Leaving through an in-app link unmounts the editor (beforeunload only covers closing or reloading the page). Pending
  // work is saved through the queue as it unmounts; keepalive lets a small one finish even if the page closes next (the
  // browser caps keepalive bodies at 64 KiB). When that save can't succeed, a link asks first.
  const leaving = onLeave(save, readOnly || !!conflicts, !!problem)
  useEffect(() => {
    flush.current = () => {
      if (!leaving.flush) return
      saver.run((cur) => {
        const body = JSON.stringify({ title, base_sha: cur.base_sha, ops, updated_at: cur.updated_at })
        return api<DraftInfo>(`/api/authoring/drafts/${id}`, { method: 'PUT', body, headers: { 'Content-Type': 'application/json' }, keepalive: body.length < 60_000 })
      }).catch(() => {}) // ponytail: the editor is gone, so a failure here has nowhere to show; offline already asked first
    }
  })
  useEffect(() => { const f = flush; return () => f.current() }, [])
  const why = leaving.ask ? saveLabel(save, now) : ''
  useEffect(() => {
    if (!why) return
    const guard = (e: MouseEvent) => {
      const a = e.target instanceof Element ? e.target.closest('a[href]') : null
      if (!(a instanceof HTMLAnchorElement) || a.target === '_blank' || a.origin !== window.location.origin) return
      if (e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return // opens elsewhere: the editor stays
      if (!window.confirm(`${why}. Leave the editor anyway? Changes that aren't saved are lost.`)) { e.preventDefault(); e.stopPropagation() }
    }
    document.addEventListener('click', guard, true) // capture: before the router follows the link
    return () => document.removeEventListener('click', guard, true)
  }, [why])

  const shown = !!active && layout.previewOpen
  const room = width - ACTIVITY - SPLIT * (shown ? 2 : 1)
  const sized = fit(layout, room, shown)
  const cols = `${ACTIVITY + sized.side}px ${SPLIT}px minmax(0, 1fr)${shown ? ` ${SPLIT}px ${sized.preview}px` : ''}`
  const activity: ['explorer' | 'problems' | 'changes' | 'blocks', string, IconName, number | undefined][] = [
    ['explorer', 'Explorer', 'files', undefined], ['problems', 'Problems', 'problems', problems.length],
    ['changes', 'Changes', 'changes', changes.length], ['blocks', 'Blocks', 'blocks', undefined],
  ]

  if (fatal) return <ErrorBox error={fatal} />
  if (!draft) return <Loader label="Heating the editor…" />
  return (
    <section className="ide" aria-label={`Editing ${draft.training}`}>
      <header className="ide-bar">
        <Link to="/edits">All edits</Link>
        <strong>{draft.training}</strong>
        <label>Title <input value={title} maxLength={200} disabled={readOnly || !!conflicts} onChange={(e) => { setTitle(e.target.value); setSave((s) => (canAutosave(s, resolving.current) ? { kind: 'dirty' } : s)) }} /></label>
        {draft.base_sha !== draft.head_sha && draft.state !== 'in_review' && <button disabled={!!conflicts || save.kind === 'conflict'} onClick={rebase}>Newer content: rebase draft</button>}
      </header>
      {draft.state === 'in_review' && <p role="note">This draft is in review. <Link to={`/edits/${draft.edit_id}`}>Open the edit</Link> and withdraw it to keep working here.</p>}
      {save.kind === 'conflict' && <p role="alert" className="error">{save.reason} <button onClick={() => load().catch(setFatal)}>Reload</button></p>}
      <p role="alert" className="error">{msg}</p>
      <div role="tablist" aria-label="Editor views" className="ide-views">
        {(['files', 'editor', 'preview'] as const).map((v) => <button key={v} role="tab" aria-selected={view === v} onClick={() => setView(v)}>{v[0].toUpperCase() + v.slice(1)}</button>)}
      </div>
      {conflicts ? <RebasePanel conflicts={conflicts.list} onDone={resolved} onCancel={() => { resolving.current = false; setConflicts(undefined) }} /> : (
      <div className="ide-main" data-view={view} ref={measure} style={{ '--cols': cols } as React.CSSProperties}>
        <div ref={explorerRef} className="ide-side">
          <div role="toolbar" aria-label="Panels" aria-orientation="vertical" className="ide-activity">
            {activity.map(([key, label, icon, count]) => (
              <button key={key} aria-pressed={panel === key} aria-label={count === undefined ? label : `${label} (${count})`} title={label}
                onClick={() => { setPanel(key); if (key === 'blocks') setPreselect(undefined) }}>
                <Icon name={icon} />{!!count && <span className={`act-badge${key === 'problems' ? ' bad' : ''}`} aria-hidden="true">{count}</span>}
              </button>
            ))}
          </div>
          <div className="ide-panel">
            {panel === 'explorer' && <Explorer paths={paths} greyed={greyed} changed={new Set(changes.map((c) => c.path))} active={active} readOnly={readOnly}
              onOpen={(p) => { open(p); setView('editor') }} onNew={create} onRename={rename} onDelete={remove} onNewModule={() => { setPanel('blocks'); setPreselect('module') }} />}
            {panel === 'problems' && <Pane title="Problems"><ProblemsPanel problems={problems} known={(f) => paths.includes(f)} onJump={jump} /></Pane>}
            {panel === 'blocks' && catalog && <Pane title="Blocks"><BlocksPanel groups={catalog.groups} blocks={catalog.blocks} paths={paths} read={textOf} need={need}
              preselect={preselect} readOnly={readOnly} onInsert={insert} /></Pane>}
            {panel === 'changes' && <Pane title="Changes"><ChangesPanel changes={changes} diffOf={diffOf} onOpen={(p) => { open(p); setView('editor') }} /></Pane>}
          </div>
        </div>
        <Splitter label="Resize the side panel" value={sized.side} {...bounds(layout, room, shown, 'side')} dir={1}
          onSize={(px) => setLayout((l) => resize(l, room, shown, 'side', px))} onReset={() => setLayout((l) => ({ ...l, side: DEFAULT_LAYOUT.side }))} />
        <div className="ide-editor">
          <div className="ide-tabs" role="toolbar" aria-label="Open files">
            {tabs.map((t) => (
              <span key={t} className={`ide-tab${t === active ? ' active' : ''}`}>
                <button aria-current={t === active ? 'true' : undefined} onClick={() => setActive(t)} title={t}>
                  {t.slice(t.lastIndexOf('/') + 1)}{t in work.puts && <span aria-label=" (changed)"> •</span>}
                </button>
                <button className="tab-close" aria-label={`Close ${t}`} title="Close" onClick={() => close(t)}><Icon name="close" /></button>
              </span>
            ))}
            <span className="spacer" />
            {active && !layout.previewOpen && <button className="icon-btn show-preview" aria-label="Show preview" title="Show preview" onClick={() => setLayout((l) => ({ ...l, previewOpen: true }))}><Icon name="preview" /></button>}
          </div>
          {active && textOf(active) === undefined && <Loader label="Opening the file…" />}
          {active && textOf(active) !== undefined
            ? <CodeEditor draftId={id} path={active} text={textOf(active)!} readOnly={readOnly} markers={problems.filter((p) => p.file === active).map((p) => ({ line: p.line, message: p.msg }))} reveal={reveal} onChange={edit} onLeave={leave} onGoToFile={() => setGoto(true)} />
            : !active && <p className="muted ide-empty">Open a file from the explorer.</p>}
        </div>
        {shown && <Splitter label="Resize the preview" value={sized.preview} {...bounds(layout, room, shown, 'preview')} dir={-1}
          onSize={(px) => setLayout((l) => resize(l, room, shown, 'preview', px))} onReset={() => setLayout((l) => ({ ...l, preview: DEFAULT_LAYOUT.preview }))} />}
        {active && (
          <div className={`ide-preview${layout.previewOpen ? '' : ' closed'}`} data-testid="edit-preview">
            <div className="pane-head">
              <h2>Preview</h2>
              <button className="icon-btn hide-preview" aria-label="Hide preview" title="Hide preview" onClick={() => setLayout((l) => ({ ...l, previewOpen: false }))}><Icon name="close" /></button>
            </div>
            <div className="pane-body preview-body"><Preview path={active} text={textOf(active) ?? ''} read={textOf} /></div>
          </div>
        )}
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

function Pane({ title, children }: { title: string; children: React.ReactNode }) {
  return <div className="pane"><div className="pane-head"><h2>{title}</h2></div><div className="pane-body">{children}</div></div>
}

// Splitter resizes the pane on one side of it: drag it, or focus it and use the arrow keys (16 px), Home and End;
// a double-click puts the default width back. dir -1: the pane is on its right, so dragging left widens it.
function Splitter({ label, value, min, max, dir, onSize, onReset }: {
  label: string; value: number; min: number; max: number; dir: 1 | -1; onSize: (px: number) => void; onReset: () => void
}) {
  const drag = useRef<{ x: number; v: number } | null>(null)
  const [dragging, setDragging] = useState(false)
  const end = () => { drag.current = null; setDragging(false) }
  return (
    <div className={`ide-split${dragging ? ' dragging' : ''}`} role="separator" aria-orientation="vertical" aria-label={label} tabIndex={0}
      aria-valuenow={value} aria-valuemin={min} aria-valuemax={max}
      onPointerDown={(e) => {
        if (e.button !== 0) return
        e.preventDefault()
        e.currentTarget.setPointerCapture(e.pointerId)
        drag.current = { x: e.clientX, v: value }
        setDragging(true)
      }}
      onPointerMove={(e) => { if (drag.current) onSize(drag.current.v + dir * (e.clientX - drag.current.x)) }}
      onPointerUp={end} onPointerCancel={end} onLostPointerCapture={end} onDoubleClick={onReset}
      onKeyDown={(e) => {
        const to = ({ ArrowLeft: value - 16 * dir, ArrowRight: value + 16 * dir, Home: min, End: max } as Record<string, number>)[e.key]
        if (to !== undefined) { e.preventDefault(); onSize(to) }
      }} />
  )
}
