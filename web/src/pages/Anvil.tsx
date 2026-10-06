import { useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router'
import { api, ApiError } from '../api'
import { useFetch } from '../useFetch'
import type { AnvilDetail, LabEvidence, SignOff, Submission, SubmissionType, TaskEvidence } from '../types'
import { ErrorBox } from '../components/ErrorBox'
import { Loader } from '../components/Loader'
import { TranscriptView } from '../components/Transcript'
import { toast } from '../lib/alerts'
import { useMe } from '../me'

const typeLabel: Record<SubmissionType, string> = { text: 'Written answer', upload: 'Upload', signoff: 'Live sign-off', review: 'Lab review', self_reported: 'Self-reported lab' }
const kb = (n: number) => (n < 1024 ? `${n} B` : `${Math.ceil(n / 1024)} KiB`)

export function AnvilPage() {
  const [draft, setDraft] = useState({ training: '', trainee: '', type: '' })
  const [filter, setFilter] = useState(draft)
  const { data, error } = useFetch<Submission[]>(`/api/anvil?${new URLSearchParams(filter)}`, 30_000)
  const signoffs = useFetch<SignOff[]>('/api/anvil/signoffs')
  if (error) return <ErrorBox error={error} />
  if (!data) return <Loader label="Heating the anvil…" />
  return (
    <section className="page anvil">
      <h1>Anvil</h1>
      <form className="row filters" onSubmit={(e) => { e.preventDefault(); setFilter(draft) }}>
        <label>Training <input value={draft.training} placeholder="any" onChange={(e) => setDraft({ ...draft, training: e.target.value })} /></label>
        <label>Trainee <input value={draft.trainee} placeholder="email" onChange={(e) => setDraft({ ...draft, trainee: e.target.value })} /></label>
        <label>Type
          <select value={draft.type} onChange={(e) => setDraft({ ...draft, type: e.target.value })}>
            <option value="">any</option>
            {Object.entries(typeLabel).map(([k, v]) => <option key={k} value={k}>{v}</option>)}
          </select>
        </label>
        <button className="ghost" type="submit">Filter</button>
      </form>
      {data.length === 0 && <p className="muted">Nothing on the anvil. Every piece is scored.</p>}
      <ul className="queue">
        {data.map((s) => (
          <li key={s.id} className="card">
            <Link to={`/anvil/${s.id}`} aria-label={`Submission from ${s.trainee}: ${s.prompt}`}>
              <strong>{s.trainee_name || s.trainee}</strong> · {typeLabel[s.type]} · {s.training} / {s.module}
              <span className="muted"> · {new Date(s.created_at).toLocaleString()}</span>
              <p>{s.prompt}</p>
            </Link>
          </li>
        ))}
      </ul>
      <h2>Live sign-offs</h2>
      {signoffs.error && <ErrorBox error={signoffs.error} />}
      {signoffs.data?.length === 0 && <p className="muted">No live sign-offs waiting.</p>}
      <ul className="queue">
        {signoffs.data?.map((o) => <SignOffRow key={`${o.team}/${o.training}/${o.module}/${o.question}/${o.trainee}`} o={o} onDone={signoffs.reload} />)}
      </ul>
    </section>
  )
}

// The server accepts 0, so an empty box must never become Number('') = 0.
const validPoints = (v: string, max: number) => v.trim() !== '' && Number.isFinite(Number(v)) && Number(v) >= 0 && Number(v) <= max
// Another scorer got there first: show the fresh state instead of a stale form.
const lostRace = (e: unknown) => e instanceof ApiError && e.status === 409

function SignOffRow({ o, onDone }: { o: SignOff; onDone: () => void }) {
  const [notes, setNotes] = useState('')
  const [busy, setBusy] = useState(false)
  const sign = async () => {
    setBusy(true)
    try {
      await api('/api/anvil/signoffs', { method: 'POST', json: { team: o.team, training: o.training, module: o.module, question: o.question, trainee: o.trainee, notes } })
      toast(`Signed off: ${o.trainee_name || o.trainee}.`)
      onDone()
    } catch (e) {
      toast((e as Error).message)
      if (lostRace(e)) onDone()
    } finally {
      setBusy(false)
    }
  }
  return (
    <li className="card" aria-label={`Sign-off for ${o.trainee}: ${o.prompt}`}>
      <p><strong>{o.trainee_name || o.trainee}</strong> · {o.training} / {o.module} · {o.points} pts</p>
      <p>{o.prompt}</p>
      <div className="row">
        <label>Notes <input value={notes} maxLength={5000} onChange={(e) => setNotes(e.target.value)} /></label>
        <button className="primary" disabled={busy} onClick={sign}>Mark passed</button>
      </div>
    </li>
  )
}

export function AnvilDetailPage() {
  const { id } = useParams()
  const nav = useNavigate()
  const { data, error, reload } = useFetch<AnvilDetail>(`/api/anvil/${id}`)
  const [points, setPoints] = useState('')
  const [feedback, setFeedback] = useState('')
  const [busy, setBusy] = useState(false)
  const { me } = useMe()
  if (error) return <ErrorBox error={error} />
  if (!data) return <Loader label="Laying the piece on the anvil…" />
  const s = data.submission
  const decide = async (what: 'score' | 'return') => {
    setBusy(true)
    try {
      await api(`/api/anvil/${s.id}/${what}`, { method: 'POST', json: what === 'score' ? { points: Number(points), feedback } : { feedback } })
      toast(what === 'score' ? 'Scored. The trainee has been told.' : 'Returned for rework.')
      nav('/anvil')
    } catch (e) {
      toast((e as Error).message)
      if (lostRace(e)) reload()
    } finally {
      setBusy(false)
    }
  }
  const isLink = s.type === 'upload' && /^https?:\/\//i.test(s.answer) // the server only stores http(s) links
  const hintCost = data.lab?.tasks?.find((t) => t.id === s.item)?.hint_cost ?? 0
  return (
    <section className="page anvil">
      <Link to="/anvil">← Back to the Anvil</Link>
      <h1>{typeLabel[s.type]} from {s.trainee_name || s.trainee}</h1>
      <p className="muted">{s.trainee} · {s.training} / {s.module} · submitted {new Date(s.created_at).toLocaleString()}</p>
      <h2>Prompt</h2>
      <p className="pre">{s.prompt}</p>
      <h2>Rubric</h2>
      <p className="rubric pre" data-testid="rubric">{s.rubric || 'No rubric: use your judgement.'}</p>
      <h2>{s.type === 'review' ? 'Notes' : s.type === 'self_reported' ? 'Reported results' : 'Answer'}</h2>
      <div className="answer" data-testid="answer">
        {isLink ? <a href={s.answer} target="_blank" rel="noopener noreferrer">{s.answer}</a> : <p className="pre">{s.answer || '—'}</p>}
        {(s.files ?? []).map((f, i) => (
          <p key={i}><a href={`/api/submissions/${s.id}/files/${i}`}>{f.name}</a> <span className="muted">({kb(f.size)})</span></p>
        ))}
      </div>
      {(data.history ?? []).length > 0 && (
        <>
          <h2>Earlier attempts</h2>
          <ul>
            {(data.history ?? []).map((h) => (
              <li key={h.id}>
                <span className="muted">{new Date(h.created_at).toLocaleString()} · {h.status}{h.scored_by ? ` by ${h.scored_by}` : ''}</span>
                {h.feedback && <p className="pre">{h.feedback}</p>}
              </li>
            ))}
          </ul>
        </>
      )}
      {data.lab && <LabEvidenceView id={s.id} ev={data.lab} onChange={reload} />}
      {s.status === 'pending' ? (
        <div className="card score-form">
          <div className="row">
            <label>Points <input type="number" min={0} max={s.max_points} step="0.5" value={points} onChange={(e) => setPoints(e.target.value)} /></label>
            <span className="muted">of {s.max_points}</span>
          </div>
          <p className="muted">Scores are final: the trainee cannot retry this item.</p>
          {hintCost > 0 && <p className="muted">Hints the trainee revealed cost {hintCost} points; they are taken off what you award.</p>}
          <label>Feedback <textarea rows={4} maxLength={5000} value={feedback} onChange={(e) => setFeedback(e.target.value)} /></label>
          <div className="row">
            <button className="primary" disabled={busy || !validPoints(points, s.max_points)} onClick={() => decide('score')}>Score</button>
            <button className="ghost" disabled={busy || !feedback.trim()} onClick={() => decide('return')}>Return for rework</button>
          </div>
        </div>
      ) : (
        <p className="muted">Already {s.status}{s.scored_by ? ` by ${s.scored_by}` : ''}.</p>
      )}
      {me.is_admin && s.kind === 'question' && <ResetPanel s={s} onDone={reload} />}
    </section>
  )
}

function LabEvidenceView({ id, ev, onChange }: { id: number; ev: LabEvidence; onChange: () => void }) {
  const [shown, setShown] = useState<number>()
  return (
    <>
      <h2>Lab evidence {ev.self_reported && <span className="badge warn">self-reported</span>}</h2>
      {(ev.tasks ?? []).map((t) => <TaskEvidenceView key={t.id} id={id} t={t} onChange={onChange} />)}
      <h2>Terminal transcripts</h2>
      {(ev.transcripts ?? []).length === 0 && <p className="muted">No transcripts yet: a session is saved when its terminal closes.</p>}
      <ul className="transcripts">
        {(ev.transcripts ?? []).map((tr) => (
          <li key={tr.id}>
            <button className="ghost" onClick={() => setShown(shown === tr.id ? undefined : tr.id)}>
              {shown === tr.id ? 'Hide' : 'Show'} transcript: {tr.terminal}
            </button>
            <span className="muted"> {new Date(tr.started_at).toLocaleString()} · {kb(tr.bytes)}{tr.truncated ? ' · earlier output trimmed' : ''}</span>
            {shown === tr.id && <TranscriptView labId={tr.lab_id} id={tr.id} />}
          </li>
        ))}
      </ul>
    </>
  )
}

function TaskEvidenceView({ id, t, onChange }: { id: number; t: TaskEvidence; onChange: () => void }) {
  const [points, setPoints] = useState(String(t.awarded))
  const [reason, setReason] = useState('')
  const [busy, setBusy] = useState(false)
  const override = async () => {
    setBusy(true)
    try {
      await api(`/api/anvil/${id}/override`, { method: 'POST', json: { task: t.id, points: Number(points), reason } })
      toast('Override saved and audited.')
      setReason('')
      onChange()
    } catch (e) {
      toast((e as Error).message)
      if (lostRace(e)) onChange()
    } finally {
      setBusy(false)
    }
  }
  return (
    <section className="card evidence" aria-label={`Task ${t.title}`}>
      <h3>{t.title} <span className="muted">({t.kind}, {t.status})</span></h3>
      <p>Awarded <span data-testid="awarded">{t.awarded} / {t.points}</span>{t.hints_used > 0 && ` · ${t.hints_used} hint(s), −${t.hint_cost} pts`}</p>
      {t.kind !== 'review' && (t.checks ?? []).length === 0 && <p className="muted">No checks run.</p>}
      <ol className="checks">
        {(t.checks ?? []).map((c, i) => (
          <li key={i}>
            <span className={c.exit_code === 0 ? 'pass' : 'warn'}>{c.exit_code === 0 ? 'passed' : `exit ${c.exit_code}`}</span>
            <span className="muted"> {new Date(c.at).toLocaleString()}{c.self_reported ? ' · self-reported' : ''}{c.answer ? ` · answer "${c.answer}"` : ''}</span>
            <pre className="check-output">{c.output}</pre>
          </li>
        ))}
      </ol>
      {t.kind !== 'review' && (
        <div className="row">
          <label>Override points <input type="number" min={0} max={t.points} step="0.5" value={points} onChange={(e) => setPoints(e.target.value)} /></label>
          <label>Reason <input value={reason} maxLength={500} onChange={(e) => setReason(e.target.value)} /></label>
          <button className="ghost" disabled={busy || !reason.trim() || !validPoints(points, t.points)} onClick={override}>Override</button>
        </div>
      )}
    </section>
  )
}

// Admin escape hatch for "scores are final": clear the trainee's quiz attempts and/or reopen this scored answer.
function ResetPanel({ s, onDone }: { s: Submission; onDone: () => void }) {
  const [attempts, setAttempts] = useState(false)
  const [score, setScore] = useState(false)
  const [reason, setReason] = useState('')
  const [busy, setBusy] = useState(false)
  const go = async () => {
    setBusy(true)
    try {
      await api(`/api/anvil/${s.id}/reset`, { method: 'POST', json: { attempts, score, reason } })
      toast('Reset and audited. The trainee has been told.')
      setAttempts(false); setScore(false); setReason('')
      onDone()
    } catch (e) {
      toast((e as Error).message)
      if (lostRace(e)) onDone()
    } finally {
      setBusy(false)
    }
  }
  return (
    <section className="card" aria-label="Admin reset">
      <h2>Reset (admin)</h2>
      <p className="muted">Forge ranks are never lowered by a reset.</p>
      <div className="row">
        <label><input type="checkbox" checked={attempts} onChange={(e) => setAttempts(e.target.checked)} /> Clear this module&apos;s quiz attempts</label>
        <label><input type="checkbox" checked={score} disabled={s.status !== 'scored'} onChange={(e) => setScore(e.target.checked)} /> Reopen this score</label>
      </div>
      <div className="row">
        <label>Reason <input value={reason} maxLength={500} onChange={(e) => setReason(e.target.value)} /></label>
        <button className="ghost" disabled={busy || !reason.trim() || (!attempts && !score)} onClick={go}>Reset</button>
      </div>
    </section>
  )
}
