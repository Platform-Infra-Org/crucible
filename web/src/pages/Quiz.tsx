import { useEffect, useState } from 'react'
import { Link, useParams } from 'react-router'
import { api } from '../api'
import { useFetch } from '../useFetch'
import type { PublicQuestion, QuizResult, QuizView } from '../types'
import { ErrorBox } from '../components/ErrorBox'
import { Loader } from '../components/Loader'
import { SparkBurst } from '../components/SparkBurst'

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
            <label key={left}>
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
      return (
        <fieldset className={cls}>
          {legend}
          <p className="muted">A person scores this question; that arrives in a later release.</p>
        </fieldset>
      )
  }
}

export function QuizPage() {
  const { team, training, module } = useParams()
  const base = `/api/programs/${team}/${training}/modules/${module}/quiz`
  const { data, error } = useFetch<QuizView>(base)
  const [answers, setAnswers] = useState<Answers>({})
  const [result, setResult] = useState<QuizResult>()
  const [spark, setSpark] = useState(0)
  const [busy, setBusy] = useState(false)
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
    try {
      const r = await api<QuizResult>(`${base}/attempts`, { method: 'POST', json: { answers } })
      setResult(r)
      if (r.passed) setSpark((s) => s + 1)
    } finally {
      setBusy(false)
    }
  }
  const pct = result ? Math.round(result.percent * 100) : 0
  return (
    <section className="page">
      <Link to={`/p/${team}/${training}`}>← Back to the training</Link>
      <h1>Prove your temper</h1>
      <p className="muted">Pass mark: {Math.round(data.pass_threshold * 100)}%</p>
      {data.questions.map((q, i) => (
        <Question key={q.id} q={q} n={i + 1} value={answers[q.id]} onChange={(v) => setAnswers((a) => ({ ...a, [q.id]: v }))} verdict={result?.correct[q.id]} />
      ))}
      <div className="row">
        <button className="primary" disabled={busy} onClick={submit}>Submit answers</button>
        <SparkBurst trigger={spark} />
      </div>
      {result && (
        <div role="status" className={`result ${result.passed ? 'pass' : 'fail'}`}>
          {result.passed ? `Passed: ${pct}%. Tempered!` : `Not yet: ${pct}%. Reheat and try again.`}{' '}
          {result.passed && <Link to={`/p/${team}/${training}`}>Back to the training</Link>}
        </div>
      )}
    </section>
  )
}
