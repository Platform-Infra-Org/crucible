import { useState, type FormEvent } from 'react'
import { Link, useParams } from 'react-router'
import { api, ApiError } from '../api'
import { useFetch } from '../useFetch'
import type { Changes, ProgramConfig, TeamView } from '../types'
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
      <p role="status" className="pass">{saved ? `Saved to git (${saved})` : ''}</p>
      {prog.can_manage && <ContentVersion key={prog.running_sha + prog.head_sha + prog.pinned_ref} team={data} prog={prog} onDone={reload} />}
      <ProgramForm key={data.platform_sha} team={data} prog={prog} onReload={reload} onStart={() => setSaved(undefined)} onSaved={(sha) => { setSaved(sha.slice(0, 7)); reload() }} />
    </section>
  )
}

function ProgramForm({ team, prog, onSaved, onReload, onStart }: { onStart: () => void; team: TeamView; prog: ProgramConfig; onSaved: (sha: string) => void; onReload: () => void }) {
  // Enrolled people who left the roster stay listed so they can be removed.
  const people = [...new Set([team.leader, ...team.seniors, ...team.members, ...team.trainees, ...prog.enrolled].map((p) => p.toLowerCase()))]
  const [enrolled, setEnrolled] = useState(() => new Set(prog.enrolled.map((p) => p.toLowerCase())))
  const [managers, setManagers] = useState(prog.roles.manager.join('\n'))
  const [scorers, setScorers] = useState(prog.roles.scorers.join('\n'))
  const [approvers, setApprovers] = useState(prog.roles.approvers.join('\n'))
  const [schedule, setSchedule] = useState(prog.schedule)
  const [ttl, setTtl] = useState(prog.lab_defaults.ttl)
  const [idle, setIdle] = useState(prog.lab_defaults.idle_timeout)
  const [ext, setExt] = useState(prog.lab_defaults.max_extension)
  const [budget, setBudget] = useState(String(prog.budget_usd_month))
  const [reviewSelf, setReviewSelf] = useState(prog.review_self_reported)
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
    if (busy) return
    setBusy(true)
    setConflict(false)
    onStart()
    try {
      const res = await api<{ sha: string }>(`/api/teams/${team.id}/programs/${prog.training}`, { method: 'PUT', json: {
        base_sha: team.platform_sha, enrolled: [...enrolled],
        roles: { manager: parseEmails(managers), scorers: parseEmails(scorers), approvers: parseEmails(approvers) },
        schedule, lab_defaults: { ttl, idle_timeout: idle, max_extension: ext }, budget_usd_month: Number(budget) || 0, review_self_reported: reviewSelf } })
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
      {!prog.can_manage && <p className="muted">Read-only — only the team leader or a program manager can change this.</p>}
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
        {prog.inline_schedule && <p>Inline schedule (edit in git): <code>{prog.inline_schedule}</code></p>}
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
        <label><input type="checkbox" checked={reviewSelf} onChange={(e) => setReviewSelf(e.target.checked)} /> Scorers review self-reported (laptop) lab results</label>
      </fieldset>
      <button className="primary" disabled={off}>Save program</button>
    </form>
  )
}

function ContentVersion({ team, prog, onDone }: { team: TeamView; prog: ProgramConfig; onDone: () => void }) {
  const [changes, setChanges] = useState<Changes>()
  const [err, setErr] = useState<string>()
  const [busy, setBusy] = useState(false)
  const [conflict, setConflict] = useState(false)
  const base = `/api/teams/${team.id}/programs/${prog.training}`
  const act = async (fn: () => Promise<void>) => {
    if (busy) return
    setBusy(true)
    setErr(undefined)
    setConflict(false)
    try { await fn() } catch (e) {
      if (reportSaveError(e)) setConflict(true)
      else setErr(e instanceof Error ? e.message : String(e))
    } finally { setBusy(false) }
  }
  const pin = (ref: string) => act(async () => {
    await api(`${base}/pin`, { method: 'PUT', json: { base_sha: team.platform_sha, ref } })
    onDone()
  })
  return (
    <section className="stack">
      <h2>Content version</h2>
      {conflict && <Conflict onReload={onDone} />}
      <p>Runs {prog.running_sha ? <code>{prog.running_sha.slice(0, 7)}</code> : 'the current version'} {prog.pinned_ref ? '(pinned)' : '(follows the branch head)'}</p>
      {prog.running_sha !== prog.head_sha && (
        <>
          <button type="button" disabled={busy} onClick={() => act(async () => setChanges(await api<Changes>(`${base}/changes`)))}>Show changes</button>
          <div role="status">
          {changes && (
            <>
              <ul>{changes.commits.map((c) => <li key={c}>{c}</li>)}</ul>
              {changes.more && <p className="muted">Showing the newest {changes.commits.length} commits; there are more.</p>}
              <pre>{changes.stat}</pre>
              <button type="button" className="primary" disabled={busy} onClick={() => pin(prog.head_sha)}>Pin to {prog.head_sha.slice(0, 7)}</button>
            </>
          )}
          </div>
        </>
      )}
      {prog.pinned_ref && <button type="button" className="ghost" disabled={busy} onClick={() => pin('')}>Follow the branch head</button>}
      {err && <p role="alert" className="fail">{err}</p>}
    </section>
  )
}
