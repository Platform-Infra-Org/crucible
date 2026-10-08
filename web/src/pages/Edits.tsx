import { useState } from 'react'
import { Link, useNavigate } from 'react-router'
import { api } from '../api'
import { useFetch } from '../useFetch'
import type { ContentEdit, DraftInfo } from '../types'
import { ErrorBox } from '../components/ErrorBox'
import { Loader } from '../components/Loader'

// EditsPage is every training's edits in one list. Each training's own edits also sit on its Manage trainings page.
export function EditsPage() {
  const nav = useNavigate()
  const trainings = useFetch<{ id: string; title: string }[]>('/api/content')
  const [pick, setPick] = useState('')
  const training = pick || trainings.data?.[0]?.id || ''
  if (trainings.error) return <ErrorBox error={trainings.error} />
  if (!trainings.data) return <Loader label="Reading the edits…" />
  return (
    <section className="page">
      <h1>Edits</h1>
      <p className="lede">Every training&apos;s drafts and edits. Each training&apos;s own are also on <Link to="/trainings/manage">Manage trainings</Link>.</p>
      <div className="toolbar fit">
        <label>Training
          <select value={training} onChange={(e) => setPick(e.target.value)}>
            {trainings.data.map((t) => <option key={t.id} value={t.id}>{t.title}</option>)}
          </select>
        </label>
        <button className="primary" disabled={!training} onClick={() => nav(`/edits/new?training=${encodeURIComponent(training)}`)}>Start an edit</button>
      </div>
      <EditList />
    </section>
  )
}

// EditList is the caller's drafts and the edits they wrote or may review: every training's, or just one training's.
export function EditList({ training }: { training?: string }) {
  const edits = useFetch<ContentEdit[]>('/api/edits')
  const drafts = useFetch<DraftInfo[]>('/api/authoring/drafts')
  const [msg, setMsg] = useState('')
  const discard = async (d: DraftInfo) => {
    if (!window.confirm(`Discard "${d.title || 'Untitled draft'}"? Your changes in it are lost.`)) return
    setMsg('')
    try { await api(`/api/authoring/drafts/${d.id}`, { method: 'DELETE' }) } catch (e) { setMsg((e as Error).message) }
    drafts.reload()
  }
  if (edits.error) return <ErrorBox error={edits.error} />
  if (drafts.error) return <ErrorBox error={drafts.error} />
  if (!edits.data) return <Loader label="Reading the edits…" />
  const H = training ? 'h5' : 'h2'
  const mine = (drafts.data ?? []).filter((d) => !training || d.training === training)
  const list = edits.data.filter((e) => !training || e.training === training)
  return (
    <>
      <H>My drafts</H>
      <p role="alert" className="error">{msg}</p>
      {mine.length === 0 && <p className="muted">No drafts. Start an edit to open the editor.</p>}
      <ul>
        {mine.map((d) => (
          <li key={d.id}>
            <Link to={`/edits/drafts/${d.id}`}>{d.title || 'Untitled draft'}</Link>{' '}
            <span className="badge">{d.state === 'in_review' ? 'in review' : d.state === 'returned' ? 'returned' : 'draft'}</span>{' '}
            {!training && <span className="muted">{d.training}</span>}{' '}
            <button className="ghost small" onClick={() => discard(d)}>Discard draft</button>
          </li>
        ))}
      </ul>
      {training && <H>Edits</H>}
      {list.length === 0 && <p className="muted">No edits yet.</p>}
      <ul>
        {list.map((e) => (
          <li key={e.id}>
            <Link to={`/edits/${e.id}`}>{e.title}</Link> <span className={`badge ${e.status}`}>{e.status}</span>{' '}
            <span className="muted">{training ? e.author : `${e.training} · ${e.author}`}</span>
          </li>
        ))}
      </ul>
    </>
  )
}
