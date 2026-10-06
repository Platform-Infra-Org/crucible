import { useState } from 'react'
import { Link, useNavigate } from 'react-router'
import { useFetch } from '../useFetch'
import type { ContentEdit } from '../types'
import { ErrorBox } from '../components/ErrorBox'
import { Loader } from '../components/Loader'

export function EditsPage() {
  const nav = useNavigate()
  const trainings = useFetch<{ id: string; title: string }[]>('/api/content')
  const edits = useFetch<ContentEdit[]>('/api/edits')
  const [pick, setPick] = useState('')
  const training = pick || trainings.data?.[0]?.id || ''
  if (trainings.error) return <ErrorBox error={trainings.error} />
  if (edits.error) return <ErrorBox error={edits.error} />
  if (!trainings.data || !edits.data) return <Loader label="Reading the edits…" />
  return (
    <section className="page">
      <h1>Edits</h1>
      <p>
        <label>Training{' '}
          <select value={training} onChange={(e) => setPick(e.target.value)}>
            {trainings.data.map((t) => <option key={t.id} value={t.id}>{t.title}</option>)}
          </select>
        </label>{' '}
        <button disabled={!training} onClick={() => nav(`/edits/new?training=${encodeURIComponent(training)}`)}>Start an edit</button>
      </p>
      {edits.data.length === 0 && <p className="muted">No edits yet.</p>}
      <ul>
        {edits.data.map((e) => (
          <li key={e.id}>
            <Link to={`/edits/${e.id}`}>{e.title}</Link> <span className={`badge ${e.status}`}>{e.status}</span>{' '}
            <span className="muted">{e.training} · {e.author}</span>
          </li>
        ))}
      </ul>
    </section>
  )
}
