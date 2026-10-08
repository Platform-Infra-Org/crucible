import { useState } from 'react'
import { Link, useParams, useSearchParams } from 'react-router'
import { api } from '../api'
import { useFetch } from '../useFetch'
import type { Heat, JourneyRow, TeamSummary } from '../types'
import { ErrorBox } from '../components/ErrorBox'
import { Loader } from '../components/Loader'
import { Cells, Flags, Legend } from '../components/HeatMap'
import { Avatar } from '../components/Avatar'
import { average, group, options, pick, type By, type Group } from '../lib/journey'
import { toast } from '../lib/alerts'
import { useMe } from '../me'

// JourneyPage is how a team is getting on: one panel per person (each training they take) or per training (everyone
// taking it), with the molten bar of how much is forged, the module heat cells and anything worth a look.
export function JourneyPage() {
  const { team } = useParams()
  const { me } = useMe()
  const [search, setSearch] = useSearchParams()
  const by: By = search.get('by') === 'training' ? 'training' : 'person'
  const f = { person: search.get('person') ?? '', training: search.get('training') ?? '', look: search.get('look') === '1' }
  // set changes one setting in the address, so a filtered view survives a reload and can be shared
  const set = (key: string, value: string) => {
    const next = new URLSearchParams(search)
    if (value) next.set(key, value)
    else next.delete(key)
    setSearch(next, { replace: true })
  }
  const { data, error } = useFetch<JourneyRow[]>(`/api/teams/${team}/journey`)
  const teams = useFetch<TeamSummary[]>('/api/teams')
  if (error) return <ErrorBox error={error} />
  if (!data) return <Loader label="Reading the heat…" />
  const name = teams.data?.find((t) => t.id === team)?.name ?? team
  const shown = pick(data, f)
  const filtered = f.person !== '' || f.training !== '' || f.look
  const opts = options(data)
  const people = new Set(shown.map((r) => r.email)).size
  const trainings = new Set(shown.map((r) => r.training)).size
  const look = shown.filter((r) => r.flags.length > 0).length
  return (
    <section className="page">
      <div className="row team-head">
        <div>
          <h1>Journey</h1>
          <p className="lede">How {name} is getting on, module by module.</p>
        </div>
        <span className="spacer" />
        <Link className="button-link" to={`/teams/${team}`}>Back to the team</Link>
      </div>
      {data.length === 0 && <p className="muted">Nothing to show for you here yet.</p>}
      {data.length > 0 && (
        <>
          <ul className="stats" aria-label="At a glance">
            <li><strong>{people}</strong> {people === 1 ? 'person' : 'people'}</li>
            <li><strong>{trainings}</strong> {trainings === 1 ? 'training' : 'trainings'}</li>
            <li><strong>{average(shown)}%</strong> forged on average</li>
            <li className={look > 0 ? 'stat-warn' : undefined}><strong>{look}</strong> {look === 1 ? 'needs' : 'need'} a look</li>
          </ul>
          <div className="toolbar journey-filters" role="search" aria-label="Filter the journey">
            <label>Person
              <select value={f.person} onChange={(e) => set('person', e.target.value)}>
                <option value="">Everyone</option>
                {opts.people.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
              </select>
            </label>
            <label>Training
              <select value={f.training} onChange={(e) => set('training', e.target.value)}>
                <option value="">Every training</option>
                {opts.trainings.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
              </select>
            </label>
            <label className="check"><input type="checkbox" checked={f.look} onChange={(e) => set('look', e.target.checked ? '1' : '')} /> Only those who need a look</label>
          </div>
          {filtered && (
            <p className="journey-showing muted">
              Showing {shown.length} of {data.length} {data.length === 1 ? 'line' : 'lines'}.{' '}
              <button type="button" className="ghost small" onClick={() => setSearch(by === 'person' ? {} : { by }, { replace: true })}>Clear filters</button>
            </p>
          )}
          <div className="row journey-bar">
            <div className="segmented" role="group" aria-label="Group the journey">
              {(['person', 'training'] as By[]).map((b) => (
                <button key={b} type="button" aria-pressed={by === b}
                  onClick={() => set('by', b === 'person' ? '' : b)}>By {b}</button>
              ))}
            </div>
            <span className="spacer" />
            <Legend />
          </div>
          {shown.length === 0 && <p className="muted">Nobody matches these filters.</p>}
          {group(shown, by).map((g) => <JourneyGroup key={g.key} g={g} by={by} />)}
        </>
      )}
      {me.is_admin && data.length > 0 && <QuizReset rows={data} />}
    </section>
  )
}

function JourneyGroup({ g, by }: { g: Group; by: By }) {
  const first = g.rows[0]
  const look = g.rows.reduce((n, r) => n + r.flags.length, 0)
  return (
    <section className="panel journey-group" aria-label={by === 'person' ? first.name || first.email : first.title}>
      <div className="journey-group-head">
        {by === 'person' ? <Avatar user={{ name: first.name, email: first.email, avatar: first.avatar ?? '' }} /> : <span className="journey-mark" aria-hidden="true">⚒</span>}
        <div>
          <h2>{by === 'person' ? first.name || first.email : first.title}</h2>
          <span className="muted">
            {by === 'person' ? `${first.email} · ${g.rows.length} ${g.rows.length === 1 ? 'training' : 'trainings'}` : `${g.rows.length} ${g.rows.length === 1 ? 'person' : 'people'}`}
          </span>
        </div>
        <span className="spacer" />
        {look > 0 && <span className="badge warn">{look} to look at</span>}
        <span className="journey-percent">{average(g.rows)}%</span>
      </div>
      {g.rows.map((r) => <JourneyLine key={r.email + r.training} r={r} label={by === 'person' ? r.title : r.name || r.email} />)}
    </section>
  )
}

function JourneyLine({ r, label }: { r: JourneyRow; label: string }) {
  const n = (h: Heat) => r.cells.filter((c) => c.heat === h).length
  return (
    <div className="journey-line" aria-label={`${r.name || r.email} in ${r.title}`} data-testid={`journey-${r.email}-${r.training}`}>
      <div className="journey-line-head">
        <strong>{label}</strong>
        <span className="muted">{n('forged')} complete, {n('glowing')} in progress, {n('cold')} not started</span>
      </div>
      <div className="journey-progress">
        <div className="molten-bar" role="progressbar" aria-label={`${label}: forged`} aria-valuenow={r.percent} aria-valuemin={0} aria-valuemax={100}>
          <div className="fill" style={{ width: `${r.percent}%` }} />
        </div>
        <span>{r.percent}%</span>
      </div>
      <Cells r={r} />
      <Flags flags={r.flags} />
    </div>
  )
}

// Admin escape hatch for a trainee locked out of a quiz (e.g. an instant-only quiz at max_attempts): clear the module's
// attempts and/or reopen its scored answers. Audited server-side; ranks are never lowered.
function QuizReset({ rows }: { rows: JourneyRow[] }) {
  const [i, setI] = useState(0)
  const row = rows[Math.min(i, rows.length - 1)]
  const [module, setModule] = useState('')
  const [attempts, setAttempts] = useState(true)
  const [score, setScore] = useState(false)
  const [reason, setReason] = useState('')
  const [busy, setBusy] = useState(false)
  const mod = row.cells.some((c) => c.module === module) ? module : row.cells[0]?.module ?? ''
  const go = async () => {
    setBusy(true)
    try {
      await api('/api/admin/quiz-reset', { method: 'POST', json: { email: row.email, team: row.team, training: row.training, module: mod, attempts, score, reason } })
      toast('Reset and audited. The trainee has been told.')
      setReason('')
    } catch (e) {
      toast((e as Error).message)
    } finally {
      setBusy(false)
    }
  }
  return (
    <section className="card" aria-label="Reset a quiz">
      <h2>Reset a quiz (admin)</h2>
      <p className="muted">Forge ranks are never lowered by a reset.</p>
      <div className="row">
        <label>Trainee <select value={i} onChange={(e) => setI(Number(e.target.value))}>
          {rows.map((r, n) => <option key={r.email + r.training} value={n}>{r.name || r.email} · {r.title}</option>)}
        </select></label>
        <label>Module <select value={mod} onChange={(e) => setModule(e.target.value)}>
          {row.cells.map((c) => <option key={c.module} value={c.module}>{c.title}</option>)}
        </select></label>
      </div>
      <div className="row">
        <label><input type="checkbox" checked={attempts} onChange={(e) => setAttempts(e.target.checked)} /> Clear quiz attempts</label>
        <label><input type="checkbox" checked={score} onChange={(e) => setScore(e.target.checked)} /> Reopen scored answers</label>
      </div>
      <div className="row">
        <label>Reason <input value={reason} maxLength={500} onChange={(e) => setReason(e.target.value)} /></label>
        <button className="ghost" disabled={busy || !mod || !reason.trim() || (!attempts && !score)} onClick={go}>Reset</button>
      </div>
    </section>
  )
}
