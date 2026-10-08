import { useState, type FormEvent, type ReactNode } from 'react'
import { Link, NavLink, useNavigate, useParams, useSearchParams } from 'react-router'
import { api } from '../api'
import { useFetch } from '../useFetch'
import type { Changes, ManagedTraining, Roles, TeamProgram, TeamRoster, TrainingsAdmin } from '../types'
import { ErrorBox } from '../components/ErrorBox'
import { Loader } from '../components/Loader'
import { Conflict, reportSaveError } from '../components/Conflict'
import { toast } from '../lib/alerts'
import { repoWarning } from '../lib/adminConfig'
import { useMe } from '../me'
import { EditList } from './Edits'
import { addTraineeRequest, enrollPlan, enrollRequest, people, pinRequest, programChange, programFields } from '../lib/teamRequests'

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

// ManageTrainingsPage is the one place for trainings: register and repoint them (admins), start one for a team, see
// and change who is enrolled, and give out the program roles.
export function ManageTrainingsPage() {
  const { training } = useParams()
  const [search] = useSearchParams()
  const { data, error, reload } = useFetch<TrainingsAdmin>('/api/org/trainings')
  if (error) return <ErrorBox error={error} />
  if (!data) return <Loader label="Laying out the moulds…" />
  const creating = data.is_admin && search.has('new')
  const selected = creating ? undefined : data.trainings.find((t) => t.id === training) ?? (training ? undefined : data.trainings[0])
  const enrolled = (t: ManagedTraining) => t.programs.reduce((n, p) => n + p.enrolled.length, 0)
  return (
    <section className="page manage">
      <h1>Manage trainings</h1>
      <p className="lede">Start a training for a team, enroll people, hand out roles and edit the content. The content lives in each training&apos;s git repository; edits reach it after review.</p>
      <div className="manage-grid">
        <nav aria-label="Trainings" className="manage-list">
          {data.trainings.length === 0 && <p className="muted">No trainings are registered yet.</p>}
          <ul>
            {data.trainings.map((t) => (
              <li key={t.id}>
                <NavLink to={`/trainings/manage/${t.id}`} className={t.id === selected?.id ? 'active' : undefined}>
                  {t.title}
                  <small>{t.id} · {t.programs.length} {t.programs.length === 1 ? 'team' : 'teams'} · {enrolled(t)} enrolled</small>
                </NavLink>
              </li>
            ))}
          </ul>
          {data.is_admin && <Link to="/trainings/manage?new" className={creating ? 'new-training active' : 'new-training'}>+ New training</Link>}
        </nav>
        <div className="manage-detail">
          {creating && <NewTraining onDone={reload} />}
          {!creating && selected && <TrainingDetail key={selected.id} t={selected} data={data} onChange={reload} />}
          {!creating && !selected && training && <p className="warn">There is no training called {training}, or it is not one you can see.</p>}
        </div>
      </div>
    </section>
  )
}

function NewTraining({ onDone }: { onDone: () => void }) {
  const navigate = useNavigate()
  const [id, setId] = useState('')
  const [repo, setRepo] = useState('')
  const [branch, setBranch] = useState('main')
  const act = useAct(onDone)
  const save = async (e: FormEvent) => {
    e.preventDefault()
    const ok = await act.run(() => api('/api/admin/trainings', { method: 'POST', json: { id: id.trim(), repo: repo.trim(), branch: branch.trim() } }),
      `${id.trim()} is registered.`)
    if (ok) navigate(`/trainings/manage/${encodeURIComponent(id.trim())}`)
  }
  return (
    <form className="stack panel" onSubmit={save}>
      <h2>New training</h2>
      <p className="muted">A training&apos;s readings, quizzes and labs live in a git repository (<code>training.yaml</code> and <code>modules/…</code>).
        Register the repository here; who takes the training and who runs it are kept in Crucible.</p>
      <label>Training id <input required value={id} placeholder="e.g. forge-501" onChange={(e) => setId(e.target.value)} /></label>
      <label>Repository URL <input required value={repo} placeholder="https://git.example.com/trainings/forge-501.git" onChange={(e) => setRepo(e.target.value)} /></label>
      <label>Branch <input required value={branch} onChange={(e) => setBranch(e.target.value)} /></label>
      {act.banner}
      <button className="primary" disabled={act.busy}>Register training</button>
    </form>
  )
}

function TrainingDetail({ t, data, onChange }: { t: ManagedTraining; data: TrainingsAdmin; onChange: () => void }) {
  const team = (id: string) => data.teams.find((x) => x.id === id)
  const free = data.teams.filter((x) => x.can_edit_team && !t.programs.some((p) => p.team === x.id))
  return (
    <>
      <h2>{t.title}</h2>
      <p className="muted"><code>{t.id}</code>{!t.available && <> · <span className="badge warn">content not loaded yet</span></>}</p>
      {data.is_admin && <Source t={t} onChange={onChange} />}
      <ContentEdits t={t} />
      <h3>Teams running it</h3>
      {t.programs.length === 0 && <p className="muted">No team runs this training yet{free.length > 0 ? '; start it for one below.' : '.'}</p>}
      {t.programs.map((p) => {
        const tm = team(p.team)
        return tm && <ProgramPanel key={`${p.team}-${p.version}-${tm.version}`} team={tm} p={p} schedules={data.schedules} onChange={onChange} />
      })}
      {free.length > 0 && <StartForTeam training={t} teams={free} onChange={onChange} />}
    </>
  )
}

// Source is where the content comes from, for admins: repoint it, or delete the training (its repository stays).
function Source({ t, onChange }: { t: ManagedTraining; onChange: () => void }) {
  const navigate = useNavigate()
  const [editing, setEditing] = useState(false)
  const [repo, setRepo] = useState('')
  const [branch, setBranch] = useState(t.branch ?? 'main')
  const act = useAct(onChange)
  const warn = repoWarning(t.repo, repo, t.programs.length)
  const save = async (e: FormEvent) => {
    e.preventDefault()
    if (warn && !window.confirm(warn)) return
    if (await act.run(() => api('/api/admin/trainings', { method: 'POST', json: { id: t.id, repo: repo.trim(), branch: branch.trim() } }), `${t.id} now reads from the new source.`)) {
      setEditing(false)
    }
  }
  const remove = async () => {
    const teams = t.programs.map((p) => p.team).join(', ')
    if (!window.confirm(`Delete ${t.title} from Crucible?${teams ? ` It stops for ${teams}.` : ''} The repository itself is not touched, and everyone's progress is kept: register it again to bring it back.`)) return
    if (await act.run(() => api(`/api/admin/trainings/${encodeURIComponent(t.id)}`, { method: 'DELETE' }), `${t.title} is deleted.`)) {
      navigate('/trainings/manage')
    }
  }
  return (
    <div className="panel">
      <h3>Source</h3>
      <dl className="facts">
        <dt>Repository</dt><dd><code>{t.repo}</code></dd>
        <dt>Branch</dt><dd>{t.branch}</dd>
      </dl>
      {act.banner}
      {!editing && (
        <div className="row">
          <button type="button" onClick={() => { setRepo(''); setBranch(t.branch ?? 'main'); setEditing(true) }}>Change source</button>
          <button type="button" className="ghost danger-text" disabled={act.busy} onClick={remove}>Delete training</button>
        </div>
      )}
      {editing && (
        <form className="stack" onSubmit={save}>
          <label>New repository URL <input required value={repo} placeholder="the full URL, credentials included if it needs them" onChange={(e) => setRepo(e.target.value)} /></label>
          <label>Branch <input required value={branch} onChange={(e) => setBranch(e.target.value)} /></label>
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

function StartForTeam({ training, teams, onChange }: { training: ManagedTraining; teams: TeamRoster[]; onChange: () => void }) {
  const [picked, setPicked] = useState('')
  const pick = teams.some((x) => x.id === picked) ? picked : teams[0].id
  const act = useAct(onChange)
  const start = () => {
    const r = enrollRequest(pick, training.id)
    void act.run(() => api(r.path, { method: r.method, json: r.json }), `${training.title} started for ${teams.find((x) => x.id === pick)?.name}.`)
  }
  return (
    <div className="panel">
      <h3>Start it for a team</h3>
      {act.banner}
      <div className="row">
        <select aria-label="Team to start it for" value={pick} onChange={(e) => setPicked(e.target.value)}>
          {teams.map((x) => <option key={x.id} value={x.id}>{x.name}</option>)}
        </select>
        <button className="primary" disabled={act.busy} onClick={start}>Start for this team</button>
      </div>
      <p className="muted">Nobody is enrolled at first. The team leader manages and approves, seniors score, until you name others.</p>
    </div>
  )
}

function Chips({ emails, label, onRemove, busy }: { emails: string[]; label: string; onRemove?: (e: string) => void; busy: boolean }) {
  return (
    <ul className="chips" aria-label={label}>
      {emails.map((e) => (
        <li key={e} className="chip">
          {e}
          {onRemove && <button type="button" aria-label={`Remove ${e} from ${label.toLowerCase()}`} disabled={busy} onClick={() => onRemove(e)}>×</button>}
        </li>
      ))}
    </ul>
  )
}

const ROLES: { key: keyof Roles; label: string; one: string; add: string }[] = [
  { key: 'manager', label: 'Managers', one: 'manager', add: 'Add a manager' },
  { key: 'scorers', label: 'Scorers', one: 'scorer', add: 'Add a scorer' },
  { key: 'approvers', label: 'Approvers', one: 'approver', add: 'Add an approver' },
]

function ProgramPanel({ team, p, schedules, onChange }: { team: TeamRoster; p: TeamProgram; schedules: string[]; onChange: () => void }) {
  const act = useAct(onChange)
  const manage = p.can_manage
  const save = (change: Parameters<typeof programChange>[2], ok: string) => act.run(() => put(programChange(team.id, p, change)), ok)
  const stop = () => {
    if (!window.confirm(`Stop ${p.title} for ${team.name}? Everyone's progress is kept, but nobody can continue until it is started again.`)) return
    void act.run(() => api(`/api/org/teams/${team.id}/programs/${p.training}`, { method: 'DELETE' }), `${p.title} stopped for ${team.name}.`)
  }
  return (
    <section className="panel" id={`team-${team.id}`} aria-label={`${team.name} in ${p.title}`}>
      <div className="row panel-head">
        <h4>{team.name}</h4>
        <span className="muted">{p.enrolled.length} enrolled</span>
        <span className="spacer" />
        {manage && <button type="button" className="ghost" disabled={act.busy} onClick={stop}>Stop for this team</button>}
      </div>
      {act.banner}
      {!manage && <p className="muted">Read-only: the team leader, a program manager or an admin makes changes here.</p>}

      <h5>Enrolled</h5>
      {p.enrolled.length === 0 && <p className="muted">Nobody is enrolled yet.</p>}
      <Chips emails={p.enrolled} label="Enrolled" busy={act.busy}
        onRemove={manage ? (e) => void save({ enrolled: p.enrolled.filter((x) => x !== e) }, `${e} is no longer enrolled.`) : undefined} />
      {manage && <EnrollBox team={team} p={p} act={act} />}

      <h5>Roles</h5>
      <p className="muted">Leave a role empty for the default: the leader manages and approves, seniors score.</p>
      {ROLES.map((r) => (
        <RoleRow key={r.key} label={r.label} one={r.one} add={r.add} stored={p.roles[r.key]} effective={p.effective_roles?.[r.key] ?? []}
          candidates={people(team).filter((e) => !p.roles[r.key].includes(e))} busy={act.busy}
          onChange={manage ? (list, ok) => void save({ roles: { ...p.roles, [r.key]: list } }, ok) : undefined} />
      ))}

      {manage && (
        <details className="lab-settings">
          <summary>Lab settings</summary>
          <LabSettings p={p} schedules={schedules} busy={act.busy} save={save} />
        </details>
      )}
      {manage && <ContentVersion team={team.id} p={p} onDone={onChange} />}
    </section>
  )
}

function EnrollBox({ team, p, act }: { team: TeamRoster; p: TeamProgram; act: ReturnType<typeof useAct> }) {
  const [email, setEmail] = useState('')
  const [problem, setProblem] = useState('')
  const listId = `enroll-${team.id}-${p.training}`
  const candidates = people(team).filter((e) => !p.enrolled.includes(e))
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    const plan = enrollPlan(team, p, email)
    if (plan.kind === 'refuse') return setProblem(plan.reason)
    setProblem('')
    if (plan.kind === 'add-and-enroll' && !window.confirm(`${plan.email} is not on ${team.name} yet. Add them to the team as a trainee and enroll them?`)) return
    const ok = await act.run(async () => {
      if (plan.kind === 'add-and-enroll') await put(addTraineeRequest(team, plan.email))
      await put(programChange(team.id, p, { enrolled: [...p.enrolled, plan.email] }))
    }, plan.kind === 'add-and-enroll' ? `${plan.email} joined ${team.name} and is enrolled.` : `${plan.email} is enrolled.`)
    if (ok) setEmail('')
  }
  return (
    <form className="row enroll" onSubmit={submit}>
      <input aria-label={`Enroll someone in ${team.name}`} list={listId} value={email} placeholder={team.can_edit_team ? 'a team member, or a new email' : 'a team member'}
        onChange={(e) => setEmail(e.target.value)} />
      <datalist id={listId}>{candidates.map((c) => <option key={c} value={c} />)}</datalist>
      <button className="primary" disabled={act.busy || !email.trim()}>Enroll</button>
      {problem && <p role="alert" className="error">{problem}</p>}
    </form>
  )
}

function RoleRow({ label, one, add, stored, effective, candidates, busy, onChange }: {
  label: string; one: string; add: string; stored: string[]; effective: string[]; candidates: string[]; busy: boolean
  onChange?: (list: string[], ok: string) => void
}) {
  return (
    <div className="role-row">
      <span className="role-name">{label}</span>
      <div>
        {stored.length > 0
          ? <Chips emails={stored} label={label} busy={busy} onRemove={onChange && ((e) => onChange(stored.filter((x) => x !== e), `${e} is no longer a ${one}.`))} />
          : <p className="muted role-default">Default: {effective.join(', ') || 'nobody'}</p>}
        {onChange && candidates.length > 0 && (
          <select aria-label={add} value="" disabled={busy} onChange={(e) => e.target.value && onChange([...stored, e.target.value], `${e.target.value} is now a ${one}.`)}>
            <option value="">{add}…</option>
            {candidates.map((c) => <option key={c} value={c}>{c}</option>)}
          </select>
        )}
      </div>
    </div>
  )
}

function LabSettings({ p, schedules, busy, save }: {
  p: TeamProgram; schedules: string[]; busy: boolean; save: (change: Parameters<typeof programChange>[2], ok: string) => Promise<boolean>
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
      {p.inline_schedule && !schedule && <p className="muted">This program keeps its own schedule windows until you pick a named schedule.</p>}
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
      <label><input type="checkbox" checked={reviewSelf} onChange={(e) => setReviewSelf(e.target.checked)} /> Scorers review self-reported (laptop) lab results</label>
      <button className="primary" disabled={busy}>Save lab settings</button>
    </form>
  )
}

function ContentVersion({ team, p, onDone }: { team: string; p: TeamProgram; onDone: () => void }) {
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
      <p>Runs {p.running_sha ? <code>{p.running_sha.slice(0, 7)}</code> : 'the current version'} {p.pinned_ref ? '(pinned)' : '(follows the branch head)'}</p>
      {p.running_sha !== p.head_sha && p.head_sha && <button type="button" disabled={act.busy} onClick={() => void show()}>Show what changed</button>}
      <div role="status">{body}</div>
      {p.pinned_ref && <button type="button" className="ghost" disabled={act.busy} onClick={() => void pin('', 'Following the branch head again.')}>Follow the branch head</button>}
    </details>
  )
}
