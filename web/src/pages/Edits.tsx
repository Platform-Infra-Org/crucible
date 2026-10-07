import { useState } from 'react'
import { Link, useNavigate } from 'react-router'
import { api } from '../api'
import { useFetch } from '../useFetch'
import type { ContentEdit, DraftInfo } from '../types'
import { ErrorBox } from '../components/ErrorBox'
import { Loader } from '../components/Loader'

export function EditsPage() {
  const nav = useNavigate()
  const trainings = useFetch<{ id: string; title: string }[]>('/api/content')
  const edits = useFetch<ContentEdit[]>('/api/edits')
  const drafts = useFetch<DraftInfo[]>('/api/authoring/drafts')
  const [pick, setPick] = useState('')
  const [msg, setMsg] = useState('')
  const discard = async (d: DraftInfo) => {
    if (!window.confirm(`Discard "${d.title || 'Untitled draft'}"? Your changes in it are lost.`)) return
    setMsg('')
    try { await api(`/api/authoring/drafts/${d.id}`, { method: 'DELETE' }) } catch (e) { setMsg((e as Error).message) }
    drafts.reload()
  }
  const training = pick || trainings.data?.[0]?.id || ''
  if (trainings.error) return <ErrorBox error={trainings.error} />
  if (edits.error) return <ErrorBox error={edits.error} />
  if (drafts.error) return <ErrorBox error={drafts.error} />
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
      <h2>My drafts</h2>
      <p role="alert" className="error">{msg}</p>
      {(drafts.data ?? []).length === 0 && <p className="muted">No drafts. Start an edit to open the editor.</p>}
      <ul>
        {(drafts.data ?? []).map((d) => (
          <li key={d.id}>
            <Link to={`/edits/drafts/${d.id}`}>{d.title || 'Untitled draft'}</Link>{' '}
            <span className="badge">{d.state === 'in_review' ? 'in review' : d.state === 'returned' ? 'returned' : 'draft'}</span>{' '}
            <span className="muted">{d.training}</span>{' '}
            <button className="ghost small" onClick={() => discard(d)}>Discard draft</button>
          </li>
        ))}
      </ul>
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
