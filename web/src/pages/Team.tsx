import { useState, type FormEvent } from 'react'
import { Link, Navigate, useNavigate, useParams } from 'react-router'
import { api, ApiError } from '../api'
import { useFetch } from '../useFetch'
import type { TeamSummary, TeamView } from '../types'
import { ErrorBox } from '../components/ErrorBox'
import { Loader } from '../components/Loader'
import { useMe } from '../me'
import { Conflict, reportSaveError } from '../components/Conflict'
import { toast } from '../lib/alerts'
import { formatMentors } from '../lib/lists'
import { budgetRequest, rosterRequest, teamPath } from '../lib/teamRequests'

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
      <p className="lede">Each team has a leader, its people and the trainings it runs.</p>
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
      await api(`/api/admin/teams/${encodeURIComponent(id.trim())}`, { method: 'POST', json: { name: name.trim(), leader: leader.trim() } })
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
      <p className="muted">Name its leader now; add the rest of the people on the team&apos;s own page.</p>
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
  const people = 1 + data.seniors.length + data.members.length + data.trainees.length
  const enrolled = data.programs.reduce((n, p) => n + p.enrolled.length, 0)
  return (
    <section className="page">
      <div className="row team-head">
        <div>
          <h1>{data.name}</h1>
          <p className="lede">Led by {data.leader}</p>
        </div>
        <span className="spacer" />
        <Link className="button-link" to={`/teams/${data.id}/journey`}>Journey</Link>
      </div>
      <ul className="stats" aria-label="At a glance">
        <li><strong>{people}</strong> {people === 1 ? 'person' : 'people'}</li>
        <li><strong>{data.trainees.length}</strong> {data.trainees.length === 1 ? 'trainee' : 'trainees'}</li>
        <li><strong>{data.programs.length}</strong> {data.programs.length === 1 ? 'training' : 'trainings'}</li>
        <li><strong>{enrolled}</strong> enrolled</li>
      </ul>
      <Roster key={`r-${data.version}`} team={data} onSaved={reload} />
      <Programs team={data} />
      <Budget key={`b-${data.budget?.version}`} team={data} onSaved={reload} />
      {data.is_admin && <RevokeAgents people={[data.leader, ...data.seniors, ...data.members, ...data.trainees]} />}
      {data.is_admin && <DeleteTeam team={data} />}
    </section>
  )
}

const GROUPS = [['Leader', 'leader'], ['Seniors', 'seniors'], ['Members', 'members'], ['Trainees', 'trainees']] as const

function Roster({ team, onSaved }: { team: TeamView; onSaved: () => void }) {
  const [seniors, setSeniors] = useState(team.seniors.join('\n'))
  const [members, setMembers] = useState(team.members.join('\n'))
  const [trainees, setTrainees] = useState(team.trainees.join('\n'))
  const [mentors, setMentors] = useState(formatMentors(team.mentors))
  const [busy, setBusy] = useState(false)
  const [conflict, setConflict] = useState(false)
  const pairs = Object.entries(team.mentors).sort(([a], [b]) => a.localeCompare(b))
  const save = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    try {
      const r = rosterRequest(team, { seniors, members, trainees, mentors })
      await api(r.path, { method: 'PUT', json: r.json })
      toast('Roster saved')
      onSaved()
    } catch (err) {
      setConflict(reportSaveError(err))
    } finally {
      setBusy(false)
    }
  }
  return (
    <section className="panel" aria-labelledby="people">
      <h2 id="people">People</h2>
      {GROUPS.map(([label, key]) => {
        const list = key === 'leader' ? [team.leader] : team[key]
        return (
          <div key={key} className="role-row">
            <span className="role-name">{label}</span>
            {list.length > 0 ? <ul className="chips">{list.map((e) => <li key={e} className="chip">{e}</li>)}</ul> : <p className="muted role-default">Nobody yet</p>}
          </div>
        )
      })}
      <div className="role-row">
        <span className="role-name">Mentors</span>
        {pairs.length > 0
          ? <ul className="chips">{pairs.map(([t, m]) => <li key={t} className="chip">{t} → {m}</li>)}</ul>
          : <p className="muted role-default">No mentor pairs</p>}
      </div>
      {team.can_edit_team && (
        <details className="lab-settings" open={conflict}>
          <summary>Edit the roster</summary>
          <form className="stack roster-form" onSubmit={save}>
            <p className="muted">One email per line. Saving replaces the roster.</p>
            {conflict && <Conflict onReload={onSaved} message={STALE} />}
            <label>Seniors <textarea rows={3} value={seniors} onChange={(e) => setSeniors(e.target.value)} /></label>
            <label>Members <textarea rows={3} value={members} onChange={(e) => setMembers(e.target.value)} /></label>
            <label>Trainees <textarea rows={4} value={trainees} onChange={(e) => setTrainees(e.target.value)} /></label>
            <label>Mentors (one “trainee = mentor” per line) <textarea rows={4} value={mentors} onChange={(e) => setMentors(e.target.value)} /></label>
            <button className="primary" disabled={busy}>Save roster</button>
          </form>
        </details>
      )}
    </section>
  )
}

// Offboarding: an admin disconnects a person's laptop agent and revokes its pairing token (audited).
function RevokeAgents({ people }: { people: string[] }) {
  const emails = [...new Set(people.filter(Boolean))]
  return (
    <section className="panel" aria-labelledby="agents">
      <h2 id="agents">Laptop agents</h2>
      <p className="muted">Revoke a person&apos;s pairing token when they leave. Their agent is disconnected at once; labs already running end by idle or TTL.</p>
      <table className="grid">
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
      </table>
    </section>
  )
}

// DeleteTeam removes the team, its roster and the trainings it runs. Progress, scores and badges are kept.
function DeleteTeam({ team }: { team: TeamView }) {
  const navigate = useNavigate()
  const [busy, setBusy] = useState(false)
  const remove = async () => {
    if (busy || !window.confirm(`Delete ${team.name}? Its people lose their roles in it and its trainings stop. Everyone's progress is kept.`)) return
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
      <p className="muted">Removes the team, its roster, mentors and budget, and stops every training it runs. Progress, scores and badges are kept.</p>
      <button type="button" className="ghost danger-text" disabled={busy} onClick={remove}>Delete team</button>
    </section>
  )
}

// Programs lists what the team runs. Starting a training, enrolling people and roles live on the trainings page.
function Programs({ team }: { team: TeamView }) {
  const { me } = useMe()
  return (
    <section className="panel" aria-labelledby="programs">
      <div className="row panel-head">
        <h2 id="programs">Trainings</h2>
        <span className="spacer" />
        {team.can_edit_team && me.can_manage_trainings && <Link to="/trainings/manage">Start a training for this team</Link>}
      </div>
      {team.programs.length === 0 && <p className="muted">This team doesn't run any training yet.</p>}
      {team.programs.length > 0 && (
        <table className="grid">
          <thead><tr><th>Training</th><th>Enrolled</th><th>Schedule</th><th>Budget</th><th /></tr></thead>
          <tbody>
            {team.programs.map((p) => (
              <tr key={p.training}>
                <td>{p.title}</td>
                <td>{p.enrolled.length}</td>
                <td>{p.schedule || 'any time'}</td>
                <td>{p.budget_usd_month ? `${usd(p.budget_usd_month)}/month` : '—'}</td>
                <td className="actions">{me.can_manage_trainings && <Link to={`/trainings/manage/${p.training}#team-${team.id}`}>{p.can_manage ? 'Manage' : 'Details'}</Link>}</td>
              </tr>
            ))}
          </tbody>
        </table>
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
