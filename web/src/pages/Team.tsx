import { useState, type FormEvent } from 'react'
import { Link, Navigate, useNavigate, useParams } from 'react-router'
import { api, ApiError } from '../api'
import { useFetch } from '../useFetch'
import type { People, Tag, TeamSummary, TeamView } from '../types'
import { ErrorBox } from '../components/ErrorBox'
import { Loader } from '../components/Loader'
import { useMe } from '../me'
import { Conflict, reportSaveError } from '../components/Conflict'
import { toast } from '../lib/alerts'
import { formatMentors, parseMentors } from '../lib/lists'
import { budgetRequest, membersOf, retag, rosterRequest, TAGS, tagPlural, teamPath } from '../lib/teamRequests'

const usd = (n: number) => `$${n.toFixed(2)}`
const STALE = 'Someone changed this team, reload to see the latest.'

export function TeamsIndex() {
  const { data, error } = useFetch<TeamSummary[]>('/api/teams')
  const { me } = useMe()
  const [starting, setStarting] = useState(false)
  if (error) return <ErrorBox error={error} />
  if (!data) return <Loader label="Gathering the smiths…" />
  if (data.length === 1 && !me.is_admin) return <Navigate to={`/teams/${data[0].id}`} replace />
  return (
    <section className="page">
      <h1>Teams</h1>
      <p className="lede">Each team has its leaders, seniors, juniors and trainees, and the trainings it owns.</p>
      {data.length === 0 && <p className="muted">You&apos;re not on a team yet.</p>}
      <div className="cards">
        {[...data].sort((a, b) => a.name.localeCompare(b.name)).map((t) => (
          <Link key={t.id} to={`/teams/${t.id}`} className="card team-card">
            <h2>{t.name}</h2>
            <p className="muted"><code>{t.id}</code> · <span className="badge">{t.role}</span></p>
          </Link>
        ))}
        {me.is_admin && !starting && (
          <button type="button" className="card new-card" onClick={() => setStarting(true)}><span aria-hidden="true">+</span> Start a team</button>
        )}
      </div>
      {starting && <CreateTeam onCancel={() => setStarting(false)} />}
    </section>
  )
}

// An admin starts a team here; the leader and the rest of the roster follow on the team's own page.
export function CreateTeam({ onCancel }: { onCancel?: () => void }) {
  const navigate = useNavigate()
  const [id, setId] = useState('')
  const [name, setName] = useState('')
  const [leader, setLeader] = useState('')
  const [busy, setBusy] = useState(false)
  const [status, setStatus] = useState('')
  const create = async (e: FormEvent) => {
    e.preventDefault()
    if (busy) return
    setBusy(true)
    setStatus('')
    try {
      await api(`/api/admin/teams/${encodeURIComponent(id.trim())}`, { method: 'POST', json: { name: name.trim(), members: { [leader.trim().toLowerCase()]: 'leader' } } })
      navigate(`/teams/${encodeURIComponent(id.trim())}`)
    } catch (err) {
      setStatus(err instanceof ApiError && err.status === 409 ? `A team called ${id.trim()} already exists.` : (err as Error).message)
    } finally {
      setBusy(false)
    }
  }
  return (
    <form className="stack panel" onSubmit={create}>
      <h2>Start a team</h2>
      <p className="muted">Name its first leader now; add the rest of the people on the team&apos;s own page. The id names its groups too, such as <code>{id.trim() || 'platform'}_seniors</code>.</p>
      <label>Team id (short, no spaces) <input required value={id} onChange={(e) => setId(e.target.value)} /></label>
      <label>Name <input required value={name} onChange={(e) => setName(e.target.value)} /></label>
      <label>Leader email <input required type="email" value={leader} onChange={(e) => setLeader(e.target.value)} /></label>
      <div className="row">
        <button className="primary" disabled={busy}>Create team</button>
        {onCancel && <button type="button" className="ghost" onClick={onCancel}>Cancel</button>}
      </div>
      <p role="status" className="fail">{status}</p>
    </form>
  )
}

export function TeamPage() {
  const { team } = useParams()
  const { data, error, reload } = useFetch<TeamView>(teamPath(team ?? ''))
  if (error) return <ErrorBox error={error} />
  if (!data) return <Loader label="Gathering the smiths…" />
  const leaders = data.members.filter((m) => m.tag === 'leader').map((m) => m.email)
  const trainees = data.members.filter((m) => m.tag === 'trainee').length
  const participants = data.programs.reduce((n, p) => n + p.resolved.participants.length, 0)
  return (
    <section className="page">
      <div className="row team-head">
        <div>
          <h1>{data.name}</h1>
          <p className="lede">{leaders.length > 0 ? `Led by ${leaders.join(', ')}` : 'This team has no leader; an admin names one below.'}</p>
        </div>
        <span className="spacer" />
        <Link className="button-link" to={`/teams/${data.id}/journey`}>Journey</Link>
      </div>
      <ul className="stats" aria-label="At a glance">
        <li><strong>{data.members.length}</strong> {data.members.length === 1 ? 'person' : 'people'}</li>
        <li><strong>{trainees}</strong> {trainees === 1 ? 'trainee' : 'trainees'}</li>
        <li><strong>{data.programs.length}</strong> {data.programs.length === 1 ? 'training' : 'trainings'}</li>
        <li><strong>{participants}</strong> taking them</li>
      </ul>
      <Roster key={`r-${data.version}`} team={data} onSaved={reload} />
      <Programs team={data} />
      <Budget key={`b-${data.budget?.version}`} team={data} onSaved={reload} />
      {data.is_admin && <RevokeAgents people={data.members.map((m) => m.email)} />}
      {data.is_admin && <DeleteTeam team={data} />}
    </section>
  )
}

// Roster shows who holds which tag. A leader changes tags (any member, any tag, leader included) and mentor pairs; an
// admin also adds people (moving them from another team) and takes them off.
function Roster({ team, onSaved }: { team: TeamView; onSaved: () => void }) {
  const people = useFetch<People>(team.is_admin ? '/api/org/people' : null)
  const [busy, setBusy] = useState(false)
  const [conflict, setConflict] = useState(false)
  const pairs = Object.entries(team.mentors).sort(([a], [b]) => a.localeCompare(b))
  const save = async (change: Parameters<typeof rosterRequest>[1], ok: string) => {
    if (busy) return false
    setBusy(true)
    try {
      const r = rosterRequest(team, change)
      await api(r.path, { method: 'PUT', json: r.json })
      toast(ok)
      onSaved()
      return true
    } catch (err) {
      setConflict(reportSaveError(err))
      return false
    } finally {
      setBusy(false)
    }
  }
  const remove = (email: string) => {
    const members = membersOf(team)
    delete members[email]
    if (!Object.values(members).includes('leader')) return toast(`${team.name} needs a leader. Make someone else leader first.`)
    if (!window.confirm(`Take ${email} off ${team.name}? Their progress is kept.`)) return
    const mentors = Object.fromEntries(Object.entries(team.mentors).filter(([t, m]) => t !== email && m !== email))
    void save({ members, mentors }, `${email} is off ${team.name}.`)
  }
  return (
    <section className="panel" aria-labelledby="people">
      <h2 id="people">People</h2>
      {conflict && <Conflict onReload={onSaved} message={STALE} />}
      {TAGS.map((tag) => {
        const list = team.members.filter((m) => m.tag === tag).map((m) => m.email)
        return (
          <div key={tag} className="role-row">
            <span className="role-name">{tagPlural[tag]}</span>
            {list.length > 0
              ? (
                  <ul className="chips" aria-label={tagPlural[tag]}>
                    {list.map((e) => (
                      <li key={e} className="chip">
                        {e}
                        {team.is_admin && <button type="button" aria-label={`Take ${e} off the team`} disabled={busy} onClick={() => remove(e)}>×</button>}
                      </li>
                    ))}
                  </ul>
                )
              : <p className="muted role-default">Nobody yet</p>}
          </div>
        )
      })}
      <div className="role-row">
        <span className="role-name">Mentors</span>
        {pairs.length > 0
          ? <ul className="chips">{pairs.map(([t, m]) => <li key={t} className="chip">{t} → {m}</li>)}</ul>
          : <p className="muted role-default">No mentor pairs</p>}
      </div>
      {team.can_edit_team && <Retag team={team} busy={busy} save={save} />}
      {team.is_admin && <AddPerson team={team} people={people.data} busy={busy} save={save} />}
      {team.can_edit_team && (
        <details className="lab-settings">
          <summary>Mentor pairs</summary>
          <Mentors team={team} busy={busy} save={save} />
        </details>
      )}
    </section>
  )
}

type Save = (change: Parameters<typeof rosterRequest>[1], ok: string) => Promise<boolean>

const TagSelect = ({ value, onChange, label }: { value: Tag; onChange: (t: Tag) => void; label: string }) => (
  <select aria-label={label} value={value} onChange={(e) => onChange(e.target.value as Tag)}>
    {TAGS.map((t) => <option key={t} value={t}>{t}</option>)}
  </select>
)

// Retag: pick a member (autocompleted from the team as it is now) and the tag they hold from now on.
function Retag({ team, busy, save }: { team: TeamView; busy: boolean; save: Save }) {
  const [email, setEmail] = useState('')
  const [tag, setTag] = useState<Tag>('senior')
  const [problem, setProblem] = useState('')
  const listId = `members-${team.id}`
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    const r = retag(team, email, tag)
    if ('problem' in r) return setProblem(r.problem)
    setProblem('')
    if (await save({ members: r.members }, `${email.trim().toLowerCase()} is now a ${tag}.`)) setEmail('')
  }
  return (
    <form className="row retag" onSubmit={submit}>
      <span className="role-name">Change a tag</span>
      <input aria-label="Team member" list={listId} value={email} placeholder="a member of the team" onChange={(e) => setEmail(e.target.value)} />
      <datalist id={listId}>{team.members.map((m) => <option key={m.email} value={m.email}>{m.tag}</option>)}</datalist>
      <TagSelect label="New tag" value={tag} onChange={setTag} />
      <button className="primary" disabled={busy || !email.trim()}>Set tag</button>
      {problem && <p role="alert" className="error">{problem}</p>}
    </form>
  )
}

// AddPerson (admins): anyone Crucible knows, or a new email. Someone on another team moves here.
function AddPerson({ team, people, busy, save }: { team: TeamView; people?: People; busy: boolean; save: Save }) {
  const [email, setEmail] = useState('')
  const [tag, setTag] = useState<Tag>('trainee')
  const [problem, setProblem] = useState('')
  const listId = `people-${team.id}`
  const members = membersOf(team)
  const candidates = (people?.people ?? []).filter((p) => !members[p.email])
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    const who = email.trim().toLowerCase()
    if (!/^[^\s@]+@[^\s@]+$/.test(who)) return setProblem('Enter an email address, such as someone@example.com.')
    if (members[who]) return setProblem(`${who} is already on ${team.name}; change their tag above.`)
    const from = people?.people.find((p) => p.email === who)?.team
    if (from && !window.confirm(`${who} is on ${from}. Move them to ${team.name} as a ${tag}? They leave ${from} and its mentor pairs.`)) return
    setProblem('')
    if (await save({ members: { ...members, [who]: tag } }, from ? `${who} moved to ${team.name}.` : `${who} joined ${team.name}.`)) setEmail('')
  }
  return (
    <form className="row retag" onSubmit={submit}>
      <span className="role-name">Add someone</span>
      <input aria-label="Person to add" list={listId} value={email} placeholder="an email" onChange={(e) => setEmail(e.target.value)} />
      <datalist id={listId}>{candidates.map((p) => <option key={p.email} value={p.email}>{p.team ? `${p.name || p.email} · ${p.tag} on ${p.team}` : p.name || 'on no team'}</option>)}</datalist>
      <TagSelect label="Their tag" value={tag} onChange={setTag} />
      <button className="primary" disabled={busy || !email.trim()}>Add to team</button>
      {problem && <p role="alert" className="error">{problem}</p>}
    </form>
  )
}

function Mentors({ team, busy, save }: { team: TeamView; busy: boolean; save: Save }) {
  const [text, setText] = useState(formatMentors(team.mentors))
  const submit = (e: FormEvent) => {
    e.preventDefault()
    try {
      void save({ mentors: parseMentors(text) }, 'Mentor pairs saved.')
    } catch (err) {
      toast((err as Error).message)
    }
  }
  return (
    <form className="stack" onSubmit={submit}>
      <p className="muted">One “trainee = mentor” per line. The mentor is a leader, senior or junior of this team.</p>
      <label>Mentor pairs <textarea rows={4} value={text} onChange={(e) => setText(e.target.value)} /></label>
      <button className="primary" disabled={busy}>Save mentor pairs</button>
    </form>
  )
}

// Offboarding: an admin disconnects a person's laptop agent and revokes its pairing token (audited).
function RevokeAgents({ people }: { people: string[] }) {
  const emails = [...new Set(people.filter(Boolean))]
  return (
    <section className="panel" aria-labelledby="agents">
      <h2 id="agents">Laptop agents</h2>
      <p className="muted">Revoke a person&apos;s pairing token when they leave. Their agent is disconnected at once; labs already running end by idle or TTL.</p>
      <div className="table-wrap"><table className="grid">
        <tbody>
          {emails.map((email) => (
            <tr key={email}>
              <td>{email}</td>
              <td className="actions">
                <button className="ghost small" onClick={async () => {
                  if (!window.confirm(`Revoke the pairing token of ${email}?`)) return
                  try {
                    await api(`/api/admin/agent/tokens?email=${encodeURIComponent(email)}`, { method: 'DELETE' })
                    toast(`Pairing revoked for ${email}`)
                  } catch (err) {
                    reportSaveError(err)
                  }
                }}>Revoke agent</button>
              </td>
            </tr>
          ))}
        </tbody>
      </table></div>
    </section>
  )
}

// DeleteTeam removes the team and its people's tags. A team that still owns trainings is refused by the server.
function DeleteTeam({ team }: { team: TeamView }) {
  const navigate = useNavigate()
  const [busy, setBusy] = useState(false)
  const remove = async () => {
    if (busy || !window.confirm(`Delete ${team.name}? Its people leave it and lose their tags. Everyone's progress is kept.`)) return
    setBusy(true)
    try {
      await api(`/api/admin/teams/${encodeURIComponent(team.id)}`, { method: 'DELETE' })
      toast(`${team.name} is deleted.`)
      navigate('/teams')
    } catch (err) {
      reportSaveError(err)
      setBusy(false)
    }
  }
  return (
    <section className="panel danger-zone" aria-labelledby="delete-team">
      <h2 id="delete-team">Delete this team</h2>
      <p className="muted">Removes the team, its tags, mentors and budget. Give its trainings to another team or delete them first. Progress, scores and badges are kept.</p>
      <button type="button" className="ghost danger-text" disabled={busy} onClick={remove}>Delete team</button>
    </section>
  )
}

// Programs lists the trainings the team owns. Registering one, its roles and its settings live on the trainings page.
function Programs({ team }: { team: TeamView }) {
  const { me } = useMe()
  return (
    <section className="panel" aria-labelledby="programs">
      <div className="row panel-head">
        <h2 id="programs">Trainings it owns</h2>
        <span className="spacer" />
        {me.can_manage_trainings && <Link to="/trainings/manage?new">Register a training</Link>}
      </div>
      {team.programs.length === 0 && <p className="muted">This team owns no training yet. Its leaders, seniors and juniors can register one.</p>}
      {team.programs.length > 0 && (
        <div className="table-wrap"><table className="grid">
          <thead><tr><th>Training</th><th>Taking it</th><th>Schedule</th><th>Budget</th><th /></tr></thead>
          <tbody>
            {team.programs.map((p) => (
              <tr key={p.training}>
                <td>{p.title}</td>
                <td>{p.resolved.participants.length}</td>
                <td>{p.schedule || 'any time'}</td>
                <td>{p.budget_usd_month ? `${usd(p.budget_usd_month)}/month` : '—'}</td>
                <td className="actions">{me.can_manage_trainings && <Link to={`/trainings/manage/${p.training}`}>{p.can_manage ? 'Manage' : 'Details'}</Link>}</td>
              </tr>
            ))}
          </tbody>
        </table></div>
      )}
    </section>
  )
}

function Budget({ team, onSaved }: { team: TeamView; onSaved: () => void }) {
  const [monthly, setMonthly] = useState(String(team.budget?.monthly_usd ?? 0))
  const [cap, setCap] = useState(String(team.budget?.hard_cap_usd ?? 0))
  const [conflict, setConflict] = useState(false)
  const [busy, setBusy] = useState(false)
  // The server only sends the budget to people who may see it.
  if (!team.budget) return null
  const summary = team.budget.monthly_usd
    ? `${usd(team.budget.monthly_usd)} a month · hard cap ${usd(team.budget.hard_cap_usd)}`
    : 'No team budget set.'
  if (!team.is_admin) return (<section className="panel" aria-labelledby="budget"><h2 id="budget">Budget</h2><p>{summary}</p></section>)
  const save = async (e: FormEvent) => {
    e.preventDefault()
    if (busy) return
    setBusy(true)
    try {
      const r = budgetRequest(team, monthly, cap)
      await api(r.path, { method: 'PUT', json: r.json })
      toast('Budget saved')
      onSaved()
    } catch (err) {
      setConflict(reportSaveError(err))
    } finally {
      setBusy(false)
    }
  }
  return (
    <form className="stack panel" onSubmit={save} aria-labelledby="budget">
      <h2 id="budget">Budget</h2>
      <p className="muted">{summary} An alert goes out at 80%; requests that would pass the cap need an admin.</p>
      {conflict && <Conflict onReload={onSaved} message="Someone changed this budget, reload to see the latest." />}
      <label>Monthly budget (USD) <input type="number" min={0} step="0.01" value={monthly} onChange={(e) => setMonthly(e.target.value)} /></label>
      <label>Hard cap (USD, empty = the budget) <input type="number" min={0} step="0.01" value={cap} onChange={(e) => setCap(e.target.value)} /></label>
      <button className="primary" disabled={busy}>Save budget</button>
    </form>
  )
}
