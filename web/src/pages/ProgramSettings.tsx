import { useState, type FormEvent } from 'react'
import { Link, useParams } from 'react-router'
import { api, ApiError } from '../api'
import { useFetch } from '../useFetch'
import type { ProgramConfig, TeamView } from '../types'
import { ErrorBox } from '../components/ErrorBox'
import { Loader } from '../components/Loader'
import { Conflict, reportSaveError } from '../components/Conflict'
import { parseEmails } from '../lib/lists'

export function ProgramSettingsPage() {
  const { team, training } = useParams()
  const { data, error, reload } = useFetch<TeamView>(`/api/teams/${team}`)
  const [saved, setSaved] = useState<string>()
  if (error) return <ErrorBox error={error} />
  if (!data) return <Loader label="Unrolling the blueprint…" />
  const prog = data.programs.find((p) => p.training === training)
  if (!prog) return <ErrorBox error={new ApiError(404, 'This team is not enrolled in that training.')} />
  return (
    <section className="page">
      <Link to={`/teams/${data.id}`}>← {data.name}</Link>
      <h1>Program settings: {prog.title}</h1>
      {saved && <p role="status" className="pass">Saved to git ({saved})</p>}
      <ProgramForm key={data.platform_sha} team={data} prog={prog} onReload={reload} onSaved={(sha) => { setSaved(sha.slice(0, 7)); reload() }} />
    </section>
  )
}

function ProgramForm({ team, prog, onSaved, onReload }: { team: TeamView; prog: ProgramConfig; onSaved: (sha: string) => void; onReload: () => void }) {
  const people = [...new Set([team.leader, ...team.seniors, ...team.members, ...team.trainees])]
  const [enrolled, setEnrolled] = useState(() => new Set(prog.enrolled))
  const [managers, setManagers] = useState(prog.roles.manager.join('\n'))
  const [scorers, setScorers] = useState(prog.roles.scorers.join('\n'))
  const [approvers, setApprovers] = useState(prog.roles.approvers.join('\n'))
  const [schedule, setSchedule] = useState(prog.schedule)
  const [ttl, setTtl] = useState(prog.lab_defaults.ttl)
  const [idle, setIdle] = useState(prog.lab_defaults.idle_timeout)
  const [ext, setExt] = useState(prog.lab_defaults.max_extension)
  const [budget, setBudget] = useState(String(prog.budget_usd_month))
  const [busy, setBusy] = useState(false)
  const [conflict, setConflict] = useState(false)
  const toggle = (p: string, on: boolean) => {
    const next = new Set(enrolled)
    if (on) next.add(p)
    else next.delete(p)
    setEnrolled(next)
  }
  const save = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    try {
      const res = await api<{ sha: string }>(`/api/teams/${team.id}/programs/${prog.training}`, { method: 'PUT', json: {
        base_sha: team.platform_sha, enrolled: [...enrolled],
        roles: { manager: parseEmails(managers), scorers: parseEmails(scorers), approvers: parseEmails(approvers) },
        schedule, lab_defaults: { ttl, idle_timeout: idle, max_extension: ext }, budget_usd_month: Number(budget) || 0 } })
      onSaved(res.sha)
    } catch (err) {
      setConflict(reportSaveError(err))
    } finally {
      setBusy(false)
    }
  }
  const off = !prog.can_manage || busy
  return (
    <form className="stack" onSubmit={save}>
      {conflict && <Conflict onReload={onReload} />}
      <fieldset className="stack" disabled={off}>
        <legend>Enrolled</legend>
        {people.map((p) => (
          <label key={p}><input type="checkbox" checked={enrolled.has(p)} onChange={(e) => toggle(p, e.target.checked)} /> {p}</label>
        ))}
      </fieldset>
      <fieldset className="stack" disabled={off}>
        <legend>Roles (one email per line; empty = the defaults: leader manages and approves, seniors score)</legend>
        <label>Managers <textarea rows={2} value={managers} onChange={(e) => setManagers(e.target.value)} /></label>
        <label>Scorers <textarea rows={2} value={scorers} onChange={(e) => setScorers(e.target.value)} /></label>
        <label>Approvers <textarea rows={2} value={approvers} onChange={(e) => setApprovers(e.target.value)} /></label>
      </fieldset>
      <fieldset className="stack" disabled={off}>
        <legend>Labs</legend>
        <label>Schedule
          <select value={schedule} onChange={(e) => setSchedule(e.target.value)}>
            <option value="">Any time</option>
            {team.schedules.map((s) => <option key={s} value={s}>{s}</option>)}
          </select>
        </label>
        <label>Lab TTL <input value={ttl} placeholder="lab default, e.g. 2h" onChange={(e) => setTtl(e.target.value)} /></label>
        <label>Idle timeout <input value={idle} placeholder="lab default, e.g. 30m" onChange={(e) => setIdle(e.target.value)} /></label>
        <label>Max extension <input value={ext} placeholder="none, e.g. 30m" onChange={(e) => setExt(e.target.value)} /></label>
        <label>Monthly budget (USD, 0 = none) <input type="number" min={0} step="0.01" value={budget} onChange={(e) => setBudget(e.target.value)} /></label>
      </fieldset>
      <button className="primary" disabled={off}>Save program</button>
    </form>
  )
}
