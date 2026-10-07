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
import { budgetRequest, rosterRequest } from '../lib/teamRequests'

const usd = (n: number) => `$${n.toFixed(2)}`
const STALE = 'Someone changed this team, reload to see the latest.'

export function TeamsIndex() {
  const { data, error } = useFetch<TeamSummary[]>('/api/teams')
  const { me } = useMe()
  if (error) return <ErrorBox error={error} />
  if (!data) return <Loader label="Gathering the smiths…" />
  if (data.length === 1 && !me.is_admin) return <Navigate to={`/teams/${data[0].id}`} replace />
  return (
    <section className="page">
      <h1>Teams</h1>
      {data.length === 0 && <p className="muted">You're not on a team yet.</p>}
      <ul>
        {data.map((t) => (
          <li key={t.id}><Link to={`/teams/${t.id}`}>{t.name}</Link> <span className="muted">{t.role}</span></li>
        ))}
      </ul>
      {me.is_admin && <CreateTeam />}
    </section>
  )
}

// An admin starts a team here; the leader and the rest of the roster follow on the team's own page.
export function CreateTeam() {
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
    <form className="stack" onSubmit={create}>
      <h2>Start a team</h2>
      <label>Team id (short, no spaces) <input required value={id} onChange={(e) => setId(e.target.value)} /></label>
      <label>Name <input required value={name} onChange={(e) => setName(e.target.value)} /></label>
      <label>Leader email <input required type="email" value={leader} onChange={(e) => setLeader(e.target.value)} /></label>
      <button className="primary" disabled={busy}>Create team</button>
      <p role="status" className="fail">{status}</p>
    </form>
  )
}

export function TeamPage() {
  const { team } = useParams()
  const { data, error, reload } = useFetch<TeamView>(`/api/org/teams/${team}`)
  if (error) return <ErrorBox error={error} />
  if (!data) return <Loader label="Gathering the smiths…" />
  return (
    <section className="page">
      <h1>{data.name}</h1>
      <p className="lede">Led by {data.leader} · <Link to={`/teams/${data.id}/journey`}>Journey</Link></p>
      <Roster key={`r-${data.version}`} team={data} onSaved={reload} />
      <Programs key={`p-${data.id}-${data.programs.map((p) => p.version).join('.')}-${data.available_trainings.length}`} team={data} onConflict={reload} />
      {data.is_admin && <RevokeAgents people={[data.leader, ...data.seniors, ...data.members, ...data.trainees]} />}
      <Budget key={`b-${data.budget?.version}`} team={data} onSaved={reload} />
    </section>
  )
}

function Roster({ team, onSaved }: { team: TeamView; onSaved: () => void }) {
  const [seniors, setSeniors] = useState(team.seniors.join('\n'))
  const [members, setMembers] = useState(team.members.join('\n'))
  const [trainees, setTrainees] = useState(team.trainees.join('\n'))
  const [mentors, setMentors] = useState(formatMentors(team.mentors))
  const [busy, setBusy] = useState(false)
  const [conflict, setConflict] = useState(false)
  if (!team.can_edit_team) {
    return (
      <>
        <h2>People</h2>
        <dl className="facts">
          <dt>Seniors</dt><dd>{team.seniors.join(', ') || '—'}</dd>
          <dt>Members</dt><dd>{team.members.join(', ') || '—'}</dd>
          <dt>Trainees</dt><dd>{team.trainees.join(', ') || '—'}</dd>
          <dt>Mentors</dt><dd>{formatMentors(team.mentors) || '—'}</dd>
        </dl>
      </>
    )
  }
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
    <form className="stack" onSubmit={save}>
      <h2>People</h2>
      <p className="muted">One email per line. Saving replaces the roster.</p>
      {conflict && <Conflict onReload={onSaved} message={STALE} />}
      <label>Seniors <textarea rows={3} value={seniors} onChange={(e) => setSeniors(e.target.value)} /></label>
      <label>Members <textarea rows={3} value={members} onChange={(e) => setMembers(e.target.value)} /></label>
      <label>Trainees <textarea rows={4} value={trainees} onChange={(e) => setTrainees(e.target.value)} /></label>
      <label>Mentors (one “trainee = mentor” per line) <textarea rows={3} value={mentors} onChange={(e) => setMentors(e.target.value)} /></label>
      <button className="primary" disabled={busy}>Save roster</button>
    </form>
  )
}

// Offboarding: an admin disconnects a person's laptop agent and revokes its pairing token (audited).
function RevokeAgents({ people }: { people: string[] }) {
  const emails = [...new Set(people.filter(Boolean))]
  return (
    <>
      <h2>Laptop agents</h2>
      <p className="muted">Revoke a person's pairing token when they leave. Their agent is disconnected at once; labs already running end by idle or TTL.</p>
      <ul>
        {emails.map((email) => (
          <li key={email}>{email}{' '}
            <button className="ghost" onClick={async () => {
              if (!window.confirm(`Revoke the pairing token of ${email}?`)) return
              try {
                await api(`/api/admin/agent/tokens?email=${encodeURIComponent(email)}`, { method: 'DELETE' })
                toast(`Pairing revoked for ${email}`)
              } catch (err) {
                reportSaveError(err)
              }
            }}>Revoke agent</button>
          </li>
        ))}
      </ul>
    </>
  )
}

function Programs({ team, onConflict }: { team: TeamView; onConflict: () => void }) {
  const navigate = useNavigate()
  const [picked, setPick] = useState('')
  // Derive from the current list so a stale choice can never overwrite an existing program.
  const pick = team.available_trainings.some((t) => t.id === picked) ? picked : (team.available_trainings[0]?.id ?? '')
  const [busy, setBusy] = useState(false)
  const [conflict, setConflict] = useState(false)
  const enroll = async () => {
    setBusy(true)
    try {
      await api(`/api/org/teams/${team.id}/programs/${pick}`, { method: 'POST', json: {
        enrolled: [], roles: { manager: [], scorers: [], approvers: [] }, schedule: '',
        lab_defaults: { ttl: '', idle_timeout: '', max_extension: '' }, budget_usd_month: 0 } })
      navigate(`/teams/${team.id}/programs/${pick}`)
    } catch (e) {
      setConflict(reportSaveError(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <>
      <h2>Programs</h2>
      {team.programs.length === 0 && <p className="muted">This team isn't enrolled in any training yet.</p>}
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
                <td>{p.can_manage && <Link to={`/teams/${team.id}/programs/${p.training}`}>Settings</Link>}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {conflict && <Conflict onReload={() => { setConflict(false); onConflict() }} message={STALE} />}
      {team.can_edit_team && team.available_trainings.length > 0 && (
        <div className="row">
          <select aria-label="Training to enroll" value={pick} onChange={(e) => setPick(e.target.value)}>
            {team.available_trainings.map((t) => <option key={t.id} value={t.id}>{t.title}</option>)}
          </select>
          <button className="primary" disabled={busy || !pick} onClick={enroll}>Enroll the team</button>
        </div>
      )}
    </>
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
  if (!team.is_admin) return (<><h2>Budget</h2><p>{summary}</p></>)
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
    <form className="stack" onSubmit={save}>
      <h2>Budget</h2>
      <p className="muted">{summary} An alert goes out at 80%; requests that would pass the cap need an admin.</p>
      {conflict && <Conflict onReload={onSaved} message="Someone changed this budget, reload to see the latest." />}
      <label>Monthly budget (USD) <input type="number" min={0} step="0.01" value={monthly} onChange={(e) => setMonthly(e.target.value)} /></label>
      <label>Hard cap (USD, empty = the budget) <input type="number" min={0} step="0.01" value={cap} onChange={(e) => setCap(e.target.value)} /></label>
      <button className="primary" disabled={busy}>Save budget</button>
    </form>
  )
}
