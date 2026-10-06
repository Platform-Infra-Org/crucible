import { useEffect, useState } from 'react'
import { Link, useParams } from 'react-router'
import { api, upload } from '../api'
import { useFetch } from '../useFetch'
import type { PublicQuestion, QuizResult, QuizView } from '../types'
import { FeedbackBox } from '../components/Feedback'
import { ErrorBox } from '../components/ErrorBox'
import { Loader } from '../components/Loader'
import { SparkBurst } from '../components/SparkBurst'
import { attemptPassed, resultMessage } from '../lib/quizResult'

type Answers = Record<string, unknown>


function Question({ q, n, value, onChange, verdict }: { q: PublicQuestion; n: number; value: unknown; onChange: (v: unknown) => void; verdict?: boolean }) {
  const cls = `question ${verdict === undefined ? '' : verdict ? 'right' : 'wrong'}`
  const legend = (
    <legend>
      {n}. {q.prompt} {verdict !== undefined && <span aria-label={verdict ? 'correct' : 'incorrect'}>{verdict ? '✓' : '✗'}</span>}
    </legend>
  )
  switch (q.type) {
    case 'single':
      return (
        <fieldset className={cls}>
          {legend}
          {q.options!.map((o) => (
            <label key={o.id}>
              <input type="radio" name={q.id} checked={value === o.id} onChange={() => onChange(o.id)} /> {o.text}
            </label>
          ))}
        </fieldset>
      )
    case 'multi': {
      const set = new Set((value as number[] | undefined) ?? [])
      return (
        <fieldset className={cls}>
          {legend}
          {q.options!.map((o) => (
            <label key={o.id}>
              <input type="checkbox" checked={set.has(o.id)} onChange={(e) => { const s = new Set(set); if (e.target.checked) s.add(o.id); else s.delete(o.id); onChange([...s]) }} /> {o.text}
            </label>
          ))}
        </fieldset>
      )
    }
    case 'exact':
    case 'regex':
      return (
        <fieldset className={cls}>
          {legend}
          <input data-testid={`answer-${q.id}`} aria-label={q.prompt} value={(value as string) ?? ''} onChange={(e) => onChange(e.target.value)} />
        </fieldset>
      )
    case 'order': {
      const ids = (value as number[]) ?? q.options!.map((o) => o.id)
      const text = (id: number) => q.options!.find((o) => o.id === id)!.text
      const move = (i: number, d: number) => { const next = [...ids]; [next[i], next[i + d]] = [next[i + d], next[i]]; onChange(next) }
      return (
        <fieldset className={cls}>
          {legend}
          <ol className="order-list" data-testid={`order-${q.id}`}>
            {ids.map((id, i) => (
              <li key={id}>
                <span>{text(id)}</span>
                <button type="button" className="ghost" aria-label={`Move ${text(id)} up`} disabled={i === 0} onClick={() => move(i, -1)}>↑</button>
                <button type="button" className="ghost" aria-label={`Move ${text(id)} down`} disabled={i === ids.length - 1} onClick={() => move(i, 1)}>↓</button>
              </li>
            ))}
          </ol>
        </fieldset>
      )
    }
    case 'match': {
      const picks = (value as number[]) ?? q.left!.map(() => -1)
      return (
        <fieldset className={cls}>
          {legend}
          {q.left!.map((left, i) => (
            <label key={i}>
              {left}{' '}
              <select aria-label={left} value={picks[i]} onChange={(e) => { const next = [...picks]; next[i] = Number(e.target.value); onChange(next) }}>
                <option value={-1}>Choose…</option>
                {q.right!.map((r) => <option key={r.id} value={r.id}>{r.text}</option>)}
              </select>
            </label>
          ))}
        </fieldset>
      )
    }
    default:
      return null
  }
}

function HumanQuestion({ q, n, base, onSaved }: { q: PublicQuestion; n: number; base: string; onSaved: () => void }) {
  const sub = q.submission
  const [text, setText] = useState('')
  const [files, setFiles] = useState<FileList | null>(null)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string>()
  const canAnswer = q.type !== 'signoff' && (!sub || sub.status === 'returned')
  const send = async () => {
    const form = new FormData()
    form.set('answer', text)
    for (const f of Array.from(files ?? [])) form.append('file', f)
    setBusy(true)
    setErr(undefined)
    try {
      await upload(`${base}/questions/${q.id}/answer`, form)
      setText('')
      setFiles(null)
      onSaved()
    } catch (e) {
      setErr((e as Error).message)
    } finally {
      setBusy(false)
    }
  }
  return (
    <fieldset className="question human" data-testid={`human-${q.id}`}>
      <legend>{n}. {q.prompt} <span className="muted">({q.points} pts, scored by a person)</span></legend>
      {sub && <FeedbackBox f={sub} />}
      {q.type === 'signoff' && !sub && <p className="muted">A scorer signs this off after you show them live.</p>}
      {canAnswer && (
        <>
          {q.type === 'text' ? (
            <textarea aria-label={`Answer to ${q.id}`} rows={5} maxLength={20000} value={text} onChange={(e) => setText(e.target.value)} />
          ) : (
            <>
              <input type="url" aria-label={`Link for ${q.id}`} placeholder="https://… (or attach files)" value={text} onChange={(e) => setText(e.target.value)} />
              <input type="file" multiple aria-label={`Files for ${q.id}`} onChange={(e) => setFiles(e.target.files)} />
            </>
          )}
          <div className="row">
            <button className="primary" disabled={busy} onClick={send}>{sub ? 'Resubmit for scoring' : 'Submit for scoring'}</button>
          </div>
          {err && <p className="error" role="alert">{err}</p>}
        </>
      )}
    </fieldset>
  )
}

export function QuizPage() {
  const { team, training, module } = useParams()
  const base = `/api/programs/${team}/${training}/modules/${module}/quiz`
  const { data, error, reload } = useFetch<QuizView>(base)
  const [answers, setAnswers] = useState<Answers>({})
  const [result, setResult] = useState<QuizResult>()
  const [spark, setSpark] = useState(0)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string>()
  const [gate, setGate] = useState<Pick<QuizView, 'attempts_left' | 'next_attempt_at'>>()
  useEffect(() => {
    if (!data) return
    const init: Answers = {}
    for (const q of data.questions) {
      if (q.type === 'order') init[q.id] = q.options!.map((o) => o.id)
      if (q.type === 'match') init[q.id] = q.left!.map(() => -1)
    }
    setAnswers(init)
  }, [data])
  if (error) return <ErrorBox error={error} />
  if (!data) return <Loader label="Heating the test piece…" />
  const submit = async () => {
    setBusy(true)
    setErr(undefined)
    try {
      const r = await api<QuizResult>(`${base}/attempts`, { method: 'POST', json: { answers } })
      setResult(r)
      if (r.status === 'complete' && attemptPassed(r, data.pass_threshold)) setSpark((s) => s + 1)
    } catch (e) {
      setErr((e as Error).message)
    } finally {
      setBusy(false)
      api<QuizView>(base).then(setGate, () => {}) // refresh attempts left without resetting the answers
    }
  }
  const { attempts_left, next_attempt_at } = gate ?? data
  const waiting = !!next_attempt_at && new Date(next_attempt_at) > new Date()
  const instant = data.questions.filter((q) => !q.human)
  const ok = !!result && result.status === 'complete' && attemptPassed(result, data.pass_threshold)
  return (
    <section className="page">
      <Link to={`/p/${team}/${training}`}>← Back to the training</Link>
      <h1>Prove your temper</h1>
      <p className="muted">Pass mark: {Math.round(data.pass_threshold * 100)}%</p>
      {attempts_left !== undefined && <p className="muted" data-testid="attempts-left">Attempts left: {attempts_left}</p>}
      {waiting && <p className="warn">Next attempt opens at {new Date(next_attempt_at!).toLocaleTimeString()}</p>}
      {data.status === 'pending_review' && <p className="banner">Waiting on the anvil: a scorer still has to read some of your answers.</p>}
      {instant.map((q, i) => (
        <Question key={q.id} q={q} n={i + 1} value={answers[q.id]} onChange={(v) => { setResult(undefined); setAnswers((a) => ({ ...a, [q.id]: v })) }} verdict={result?.correct[q.id]} />
      ))}
      {instant.length > 0 && (
        <div className="row">
          <button className="primary" disabled={busy || attempts_left === 0 || waiting} onClick={submit}>Submit answers</button>
          <SparkBurst trigger={spark} />
        </div>
      )}
      {data.questions.filter((q) => q.human).map((q, i) => (
        <HumanQuestion key={q.id} q={q} n={instant.length + i + 1} base={base} onSaved={reload} />
      ))}
      {err && <p className="error" role="alert">{err}</p>}
      <div role="status" className={result ? `result ${ok ? 'pass' : 'fail'}` : undefined}>
        {result && (<>
          {resultMessage(result, data.pass_threshold)}{' '}
          {result.status === 'complete' && <Link to={`/p/${team}/${training}`}>Back to the training</Link>}
        </>)}
      </div>
    </section>
  )
}
