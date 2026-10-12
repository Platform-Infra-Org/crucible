import { useState, type FormEvent, type ReactNode } from 'react'
import { Link, NavLink, useNavigate, useParams, useSearchParams } from 'react-router'
import { api } from '../api'
import { useFetch } from '../useFetch'
import type { Access, Changes, ManagedTraining, People, ProgramConfig, TrainingsAdmin } from '../types'
import { ErrorBox } from '../components/ErrorBox'
import { Loader } from '../components/Loader'
import { Conflict, reportSaveError } from '../components/Conflict'
import { toast } from '../lib/alerts'
import { repoWarning } from '../lib/adminConfig'
import { checkRef, describeGroup, isGroup } from '../lib/refs'
import { useMe } from '../me'
import { EditList } from './Edits'
import { pinRequest, programChange, programFields } from '../lib/teamRequests'

const STALE = 'Someone changed this while you were looking. Reload to see the latest.'

// useAct runs one save at a time: a toast and a reload on success, the Conflict banner on a 409, a toast otherwise.
function useAct(onChange: () => void) {
  const [busy, setBusy] = useState(false)
  const [conflict, setConflict] = useState(false)
  const run = async (fn: () => Promise<unknown>, ok: string) => {
    if (busy) return false
    setBusy(true)
    setConflict(false)
    try {
      await fn()
      toast(ok)
      onChange()
      return true
    } catch (e) {
      setConflict(reportSaveError(e))
      return false
    } finally {
      setBusy(false)
    }
  }
  const banner = conflict && <Conflict onReload={() => { setConflict(false); onChange() }} message={STALE} />
  return { busy, run, banner }
}

const put = (r: { path: string; json: unknown }) => api(r.path, { method: 'PUT', json: r.json })

// ManageTrainingsPage is the one place for trainings: register one for your team, point it at its content, give its
// roles to people and groups, and set how its labs run.
export function ManageTrainingsPage() {
  const { training } = useParams()
  const [search] = useSearchParams()
  const { data, error, reload } = useFetch<TrainingsAdmin>('/api/org/trainings')
  const people = useFetch<People>(data?.can_create ? '/api/org/people' : null)
  if (error) return <ErrorBox error={error} />
  if (!data) return <Loader label="Laying out the moulds…" />
  const creating = data.can_create && search.has('new')
  const selected = creating ? undefined : data.trainings.find((t) => t.id === training) ?? (training ? undefined : data.trainings[0])
  return (
    <section className="page manage">
      <h1>Manage trainings</h1>
      <p className="lede">Register a training for your team, choose who takes it, edits it and runs it, and edit the content. The content lives in each training&apos;s git repository; edits reach it after review.</p>
      <div className="manage-grid">
        <nav aria-label="Trainings" className="manage-list">
          {data.trainings.length === 0 && <p className="muted">You don&apos;t run or edit any training yet.</p>}
          <ul>
            {data.trainings.map((t) => (
              <li key={t.id}>
                <NavLink to={`/trainings/manage/${t.id}`} className={t.id === selected?.id ? 'active' : undefined}>
                  {t.title}
                  <small>{t.id} · {t.team || 'no team'}{t.program && ` · ${t.program.resolved.participants.length} taking it`}</small>
                </NavLink>
              </li>
            ))}
          </ul>
          {data.can_create && <Link to="/trainings/manage?new" className={creating ? 'new-training active' : 'new-training'}>+ New training</Link>}
        </nav>
        <div className="manage-detail">
          {creating && <NewTraining data={data} onDone={reload} />}
          {!creating && selected && <TrainingDetail key={selected.id} t={selected} data={data} people={people.data} onChange={reload} />}
          {!creating && !selected && training && <p className="warn">There is no training called {training}, or it is not one you can see.</p>}
        </div>
      </div>
    </section>
  )
}

function NewTraining({ data, onDone }: { data: TrainingsAdmin; onDone: () => void }) {
  const navigate = useNavigate()
  const [team, setTeam] = useState(data.my_team || data.teams[0]?.id || '')
  const [repository, setRepository] = useState(data.repos?.[0]?.id ?? 0)
  const [ref, setRef] = useState('main')
  const [path, setPath] = useState('')
  const act = useAct(onDone)
  const owner = data.is_admin ? team : data.my_team
  // The id and the title come from the repository's training.yaml. An empty repository gets a starter training
  // written into it, and the answer says which it was.
  const save = async (e: FormEvent) => {
    e.preventDefault()
    let got: { id: string; title: string; started: boolean } | undefined
    const ok = await act.run(async () => {
      got = await api<{ id: string; title: string; started: boolean }>('/api/org/trainings',
        { method: 'POST', json: { repository, team: owner, ref: ref.trim(), path: path.trim() } })
    }, '')
    if (ok && got) {
      toast(got.started
        ? `${got.id} is registered for ${owner}, with a training to start from written into the repository.`
        : `${got.title} is registered for ${owner}.`)
      navigate(`/trainings/manage/${encodeURIComponent(got.id)}`)
    }
  }
  return (
    <form className="stack panel" onSubmit={save}>
      <h2>New training</h2>
      <p className="muted">A training&apos;s readings, quizzes and labs live in a git repository (<code>training.yaml</code> and <code>modules/…</code>).
        Choose a connected repository and the folder the training is in; its id and title come from that <code>training.yaml</code>. A repository with nothing in it yet gets a training to start from, which you can
        then edit here.</p>
      {data.is_admin
        ? (
            <label>Team that owns it
              <select required value={team} onChange={(e) => setTeam(e.target.value)}>
                {data.teams.map((t) => <option key={t.id} value={t.id}>{t.name}</option>)}
              </select>
            </label>
          )
        : <p>Owned by <strong>{data.my_team}</strong>, your team.</p>}
      {data.repos && data.repos.length > 0
        ? (
            <label>Repository
              <select required value={repository} onChange={(e) => setRepository(Number(e.target.value))}>
                {data.repos.map((r) => <option key={r.id} value={r.id}>{r.url}</option>)}
              </select>
            </label>
          )
        : <p role="alert" className="warn">No repository is connected yet. An admin connects one under
            Administrator &rarr; Repositories, then a training can be registered from it.</p>}
      <RefFields gitRef={ref} setRef={setRef} path={path} setPath={setPath} />
      {act.banner}
      <button className="primary" disabled={act.busy || !owner}>Register training</button>
    </form>
  )
}

function TrainingDetail({ t, data, people, onChange }: { t: ManagedTraining; data: TrainingsAdmin; people?: People; onChange: () => void }) {
  return (
    <>
      <h2>{t.title}</h2>
      <p className="muted"><code>{t.id}</code> · {t.team ? <>owned by <Link to={`/teams/${t.team}`}>{t.team}</Link></> : 'no team owns it yet'}
        {!t.available && <> · <span className="badge warn">content not loaded yet</span></>}</p>
      {data.is_admin && <Owner t={t} data={data} onChange={onChange} />}
      {t.can_manage && <Source t={t} onChange={onChange} />}
      <ContentEdits t={t} />
      {t.program && <Run key={t.program.version} p={t.program} people={people} schedules={data.schedules} onChange={onChange} />}
    </>
  )
}

// Owner (admins): give the training to another team. Its run (roles, version, schedule, budget) moves with it.
function Owner({ t, data, onChange }: { t: ManagedTraining; data: TrainingsAdmin; onChange: () => void }) {
  const others = data.teams.filter((x) => x.id !== t.team)
  const [picked, setPicked] = useState('')
  const act = useAct(onChange)
  if (others.length === 0) return null
  const pick = others.some((x) => x.id === picked) ? picked : others[0].id
  const give = () => {
    if (t.team && !window.confirm(`Give ${t.title} to ${pick}? Its roles, version, schedule and budget move with it; ${t.team}'s leaders, seniors and juniors stop managing it.`)) return
    void act.run(() => api(`/api/admin/trainings/${encodeURIComponent(t.id)}/owner`, { method: 'PUT', json: { team: pick } }), `${t.title} now belongs to ${pick}.`)
  }
  return (
    <div className="panel">
      <h3>Team</h3>
      {act.banner}
      <div className="row">
        <select aria-label="Team to give it to" value={pick} onChange={(e) => setPicked(e.target.value)}>
          {others.map((x) => <option key={x.id} value={x.id}>{x.name}</option>)}
        </select>
        <button className={t.team ? 'ghost' : 'primary'} disabled={act.busy} onClick={give}>{t.team ? 'Give to this team' : 'Give it to this team'}</button>
      </div>
    </div>
  )
}

// Source is where the content comes from: repoint it, or delete the training (its repository stays).
function Source({ t, onChange }: { t: ManagedTraining; onChange: () => void }) {
  const navigate = useNavigate()
  const [editing, setEditing] = useState(false)
  const [repo, setRepo] = useState('')
  const [ref, setRef] = useState(t.ref ?? 'main')
  const [path, setPath] = useState(t.path ?? '')
  const act = useAct(onChange)
  const warn = repoWarning(t, repo, path, !!t.program?.pinned_ref)
  const save = async (e: FormEvent) => {
    e.preventDefault()
    if (warn && !window.confirm(warn)) return
    if (await act.run(() => api(`/api/org/trainings/${encodeURIComponent(t.id)}/source`, { method: 'PUT', json: { repo: repo.trim(), ref: ref.trim(), path: path.trim() } }), `${t.id} now reads from the new source.`)) {
      setEditing(false)
    }
  }
  const remove = async () => {
    if (!window.confirm(`Delete ${t.title} from Crucible? Nobody can take it any more. The repository itself is not touched, and everyone's progress is kept: register it again to bring it back.`)) return
    if (await act.run(() => api(`/api/org/trainings/${encodeURIComponent(t.id)}`, { method: 'DELETE' }), `${t.title} is deleted.`)) {
      navigate('/trainings/manage')
    }
  }
  return (
    <div className="panel">
      <h3>Source</h3>
      <dl className="facts">
        <dt>Repository</dt><dd><code>{t.repo}</code></dd>
        <dt>Branch, tag or commit</dt><dd><code>{t.ref}</code></dd>
        <dt>Folder</dt><dd>{t.path ? <code>{t.path}/</code> : 'the repository root'}</dd>
      </dl>
      {act.banner}
      {!editing && (
        <div className="row">
          <button type="button" onClick={() => { setRepo(''); setRef(t.ref ?? 'main'); setPath(t.path ?? ''); setEditing(true) }}>Change source</button>
          <button type="button" className="ghost danger-text" disabled={act.busy} onClick={remove}>Delete training</button>
        </div>
      )}
      {editing && (
        <form className="stack" onSubmit={save}>
          <label>New repository URL <input value={repo} placeholder="empty keeps the current one; a new URL with its credentials, if it needs them" onChange={(e) => setRepo(e.target.value)} /></label>
          <RefFields gitRef={ref} setRef={setRef} path={path} setPath={setPath} />
          {warn && <p role="alert" className="warn">{warn}</p>}
          <div className="row">
            <button className="primary" disabled={act.busy}>{warn ? 'Change anyway' : 'Save source'}</button>
            <button type="button" className="ghost" onClick={() => setEditing(false)}>Cancel</button>
          </div>
        </form>
      )}
    </div>
  )
}

// RefFields picks what a training follows inside its repository: a ref, and the folder that holds training.yaml.
function RefFields({ gitRef, setRef, path, setPath }: { gitRef: string; setRef: (v: string) => void; path: string; setPath: (v: string) => void }) {
  return (
    <>
      <label>Branch, tag or commit <input required value={gitRef} placeholder="main" onChange={(e) => setRef(e.target.value)} /></label>
      <p className="muted">A branch follows its newest commit and takes content edits from Crucible. A tag or a commit stays on that version; change it in git.</p>
      <label>Folder <input value={path} placeholder="empty for the repository root, or e.g. trainings/forge-501" onChange={(e) => setPath(e.target.value)} /></label>
    </>
  )
}

// ContentEdits is the training's content side: open the editor on it, and its drafts and edits waiting on you.
function ContentEdits({ t }: { t: ManagedTraining }) {
  const { me } = useMe()
  const editable = useFetch<{ id: string }[]>(me.can_edit_content ? '/api/content' : null)
  if (!me.can_edit_content) return null
  const canEdit = !!editable.data?.some((x) => x.id === t.id)
  return (
    <section className="panel" aria-label="Content edits">
      <div className="row panel-head">
        <h3>Content edits</h3>
        <span className="spacer" />
        <Link className="muted" to="/edits">All trainings&apos; edits</Link>
        {canEdit && <Link className="button-link" to={`/edits/new?training=${encodeURIComponent(t.id)}`}>Edit content</Link>}
      </div>
      <p className="muted">Changes to the readings, quizzes and labs go to review, then into the training&apos;s git repository.</p>
      <EditList training={t.id} />
    </section>
  )
}

const ROLES: { key: keyof Access; label: string; one: string; what: string; already?: (team: string) => string }[] = [
  { key: 'participants', label: 'Participants', one: 'a participant', what: 'take the training' },
  {
    key: 'editors', label: 'Editors', one: 'an editor', what: 'propose and review content edits, and score answers',
    already: (team) => `${team ? `${team}'s leaders and seniors` : 'the owning team\u2019s leaders and seniors'} already do, as do the maintainers named in training.yaml and Crucible admins.`,
  },
  {
    key: 'admins', label: 'Admins', one: 'an admin', what: 'do what editors do, manage the training and approve its paid labs',
    already: (team) => `${team ? `${team}'s leaders, seniors and juniors` : 'the owning team\u2019s leaders, seniors and juniors'} already manage it, as do Crucible admins.`,
  },
]

// Run is the training's one run: who holds each role, how its labs run, and the content version it uses.
function Run({ p, people, schedules, onChange }: { p: ProgramConfig; people?: People; schedules: string[]; onChange: () => void }) {
  const act = useAct(onChange)
  const manage = p.can_manage
  const save = (change: Parameters<typeof programChange>[1], ok: string) => act.run(() => put(programChange(p, change)), ok)
  return (
    <section className="panel" aria-label={`Roles in ${p.title}`}>
      <div className="row panel-head">
        <h3>Roles</h3>
        <span className="spacer" />
        <span className="muted">{p.resolved.participants.length} taking it</span>
      </div>
      {act.banner}
      {manage
        ? <p className="muted">Give a role to anyone, or to a group: <code>all</code>, <code>all_seniors</code>, <code>{p.team}</code>, <code>{p.team}_trainees</code>… Groups follow the teams as they change.</p>
        : <p className="muted">Read-only: {p.team}&apos;s leaders, seniors and juniors and the training&apos;s admins make changes here.</p>}
      {ROLES.map((r) => (
        <RoleRow key={r.key} id={`${p.training}-${r.key}`} label={r.label} one={r.one} what={r.what} refs={p.roles[r.key]} resolved={p.resolved[r.key]}
          already={r.already?.(p.team)} people={people} busy={act.busy}
          onChange={manage ? (list, ok) => void save({ roles: { ...p.roles, [r.key]: list } }, ok) : undefined} />
      ))}
      {manage && (
        <details className="lab-settings">
          <summary>Lab settings</summary>
          <LabSettings p={p} schedules={schedules} busy={act.busy} save={save} />
        </details>
      )}
      {manage && <ContentVersion p={p} onDone={onChange} />}
    </section>
  )
}

function RoleRow({ id, label, one, what, already, refs, resolved, people, busy, onChange }: {
  id: string; label: string; one: string; what: string; already?: string; refs: string[]; resolved: string[]
  people?: People; busy: boolean
  onChange?: (list: string[], ok: string) => void
}) {
  const [text, setText] = useState('')
  const [problem, setProblem] = useState('')
  const listId = `refs-${id}`
  const groups = people?.groups ?? []
  const add = (e: FormEvent) => {
    e.preventDefault()
    const r = checkRef(text, groups)
    if ('problem' in r) return setProblem(r.problem)
    if (refs.includes(r.ref)) return setProblem(`${r.ref} is already ${one}.`)
    setProblem('')
    setText('')
    onChange?.([...refs, r.ref], `${r.ref} ${isGroup(r.ref) ? 'are' : 'is'} now ${one}${isGroup(r.ref) ? 's' : ''}.`)
  }
  return (
    <div className="role-row">
      <span className="role-name">{label}</span>
      <div>
        <p className="muted role-default">They {what}.{refs.some(isGroup) && ` Now ${resolved.length} ${resolved.length === 1 ? 'person' : 'people'}.`}</p>
        {refs.length === 0 && <p className="muted role-default">{already ? `Nobody named \u2014 ${already}` : 'Nobody yet.'}</p>}
        <ul className="chips" aria-label={label}>
          {refs.map((ref) => (
            <li key={ref} className={isGroup(ref) ? 'chip group' : 'chip'} title={isGroup(ref) ? describeGroup(ref) : undefined}>
              {isGroup(ref) && <span aria-hidden="true">⚑ </span>}{ref}
              {onChange && <button type="button" aria-label={`Remove ${ref} from ${label.toLowerCase()}`} disabled={busy}
                onClick={() => onChange(refs.filter((x) => x !== ref), `${ref} ${isGroup(ref) ? 'are' : 'is'} no longer ${one}${isGroup(ref) ? 's' : ''}.`)}>×</button>}
            </li>
          ))}
        </ul>
        {onChange && (
          <form className="row enroll" onSubmit={add}>
            <input aria-label={`Add to ${label.toLowerCase()}`} list={listId} value={text} placeholder="an email or a group" onChange={(e) => setText(e.target.value)} />
            <datalist id={listId}>
              {groups.filter((g) => !refs.includes(g)).map((g) => <option key={g} value={g}>{describeGroup(g)}</option>)}
              {(people?.people ?? []).filter((x) => !refs.includes(x.email)).map((x) => (
                <option key={x.email} value={x.email}>{[x.name, x.team && `${x.tag} on ${x.team}`].filter(Boolean).join(' · ') || 'on no team'}</option>
              ))}
            </datalist>
            <button className="primary" disabled={busy || !text.trim()}>Add</button>
            {problem && <p role="alert" className="error">{problem}</p>}
          </form>
        )}
      </div>
    </div>
  )
}

function LabSettings({ p, schedules, busy, save }: {
  p: ProgramConfig; schedules: string[]; busy: boolean; save: (change: Parameters<typeof programChange>[1], ok: string) => Promise<boolean>
}) {
  const f = programFields(p)
  const [schedule, setSchedule] = useState(f.schedule)
  const [ttl, setTtl] = useState(f.ttl)
  const [idle, setIdle] = useState(f.idle)
  const [ext, setExt] = useState(f.ext)
  const [budget, setBudget] = useState(f.budget)
  const [reviewSelf, setReviewSelf] = useState(f.reviewSelf)
  const submit = (e: FormEvent) => {
    e.preventDefault()
    void save({ schedule, ttl, idle, ext, budget, reviewSelf }, 'Lab settings saved.')
  }
  return (
    <form className="stack" onSubmit={submit}>
      {p.inline_schedule && !schedule && <p className="muted">This training keeps its own schedule windows until you pick a named schedule.</p>}
      <label>Schedule
        <select value={schedule} onChange={(e) => setSchedule(e.target.value)}>
          <option value="">Any time</option>
          {schedules.map((s) => <option key={s} value={s}>{s}</option>)}
        </select>
      </label>
      <label>Lab time limit <input value={ttl} placeholder="the lab's own, e.g. 2h" onChange={(e) => setTtl(e.target.value)} /></label>
      <label>Idle timeout <input value={idle} placeholder="the lab's own, e.g. 30m" onChange={(e) => setIdle(e.target.value)} /></label>
      <label>Longest extension <input value={ext} placeholder="none, e.g. 30m" onChange={(e) => setExt(e.target.value)} /></label>
      <label>Monthly budget (USD, 0 = none) <input type="number" min={0} step="0.01" value={budget} onChange={(e) => setBudget(e.target.value)} /></label>
      <label><input type="checkbox" checked={reviewSelf} onChange={(e) => setReviewSelf(e.target.checked)} /> Editors and admins review self-reported (laptop) lab results</label>
      <button className="primary" disabled={busy}>Save lab settings</button>
    </form>
  )
}

function ContentVersion({ p, onDone }: { p: ProgramConfig; onDone: () => void }) {
  const team = p.team
  const [changes, setChanges] = useState<Changes>()
  const act = useAct(onDone)
  const pin = (sha: string, ok: string) => act.run(() => put(pinRequest(team, p.training, sha)), ok)
  const show = async () => {
    try { setChanges(await api<Changes>(`/api/teams/${team}/programs/${p.training}/changes`)) } catch (e) { reportSaveError(e) }
  }
  let body: ReactNode = null
  if (changes) {
    body = (
      <>
        <ul>{changes.commits.map((c) => <li key={c}>{c}</li>)}</ul>
        {changes.more && <p className="muted">Showing the newest {changes.commits.length} commits; there are more.</p>}
        <pre>{changes.stat}</pre>
        <button type="button" className="primary" disabled={act.busy} onClick={() => void pin(p.head_sha, `Pinned to ${p.head_sha.slice(0, 7)}.`)}>Pin to {p.head_sha.slice(0, 7)}</button>
      </>
    )
  }
  return (
    <details className="lab-settings">
      <summary>Content version</summary>
      {act.banner}
      <p>Runs {p.running_sha ? <code>{p.running_sha.slice(0, 7)}</code> : 'the current version'} {p.pinned_ref ? '(pinned)' : '(follows the source head)'}</p>
      {p.running_sha !== p.head_sha && p.head_sha && <button type="button" disabled={act.busy} onClick={() => void show()}>Show what changed</button>}
      <div role="status">{body}</div>
      {p.pinned_ref && <button type="button" className="ghost" disabled={act.busy} onClick={() => void pin('', 'Following the source head again.')}>Follow the source head</button>}
    </details>
  )
}
