import { useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router'
import { api } from '../api'
import { useFetch } from '../useFetch'
import { ErrorBox } from '../components/ErrorBox'
import { Loader } from '../components/Loader'
import { Markdown } from '../components/Markdown'

export function ReadingPage() {
  const { team, training, module, item } = useParams()
  const nav = useNavigate()
  const path = `/api/programs/${team}/${training}/modules/${module}/reading/${item}`
  const { data, error } = useFetch<{ title: string; markdown: string }>(path)
  const [busy, setBusy] = useState(false)
  if (error) return <ErrorBox error={error} />
  if (!data) return <Loader label="Unrolling the scroll…" />
  const markRead = async () => {
    setBusy(true)
    await api(`${path}/read`, { method: 'POST' })
    nav(`/p/${team}/${training}`)
  }
  return (
    <article className="page">
      <Link to={`/p/${team}/${training}`}>← Back to the training</Link>
      <Markdown text={data.markdown} assetBase={`/api/programs/${team}/${training}/assets`} />
      <button className="primary" disabled={busy} onClick={markRead}>Mark as read</button>
    </article>
  )
}
