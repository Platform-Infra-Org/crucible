import { useState } from 'react'
import { useParams } from 'react-router'
import { api } from '../api'
import { useFetch } from '../useFetch'
import type { JourneyRow } from '../types'
import { ErrorBox } from '../components/ErrorBox'
import { Loader } from '../components/Loader'
import { HeatMap, Legend } from '../components/HeatMap'
import { toast } from '../lib/alerts'
import { useMe } from '../me'

export function JourneyPage() {
  const { team } = useParams()
  const { me } = useMe()
  const { data, error } = useFetch<JourneyRow[]>(`/api/teams/${team}/journey`)
  if (error) return <ErrorBox error={error} />
  if (!data) return <Loader label="Reading the heat…" />
  return (
    <section className="page">
      <h1>Journey</h1>
      <Legend />
      <HeatMap rows={data} />
      {me.is_admin && data.length > 0 && <QuizReset rows={data} />}
    </section>
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
