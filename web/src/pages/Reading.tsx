import { useEffect, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router'
import { api } from '../api'
import { useFetch } from '../useFetch'
import { ErrorBox } from '../components/ErrorBox'
import { Loader } from '../components/Loader'
import { Markdown } from '../components/Markdown'
import { MoltenBar } from '../components/MoltenBar'

export function ReadingPage() {
  const { team, training, module, item } = useParams()
  const nav = useNavigate()
  const path = `/api/programs/${team}/${training}/modules/${module}/reading/${item}`
  const { data, error } = useFetch<{ title: string; markdown: string }>(path)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string>()
  const [depth, setDepth] = useState(0)
  useEffect(() => {
    const on = () => {
      const el = document.documentElement
      const max = el.scrollHeight - el.clientHeight
      setDepth(max <= 0 ? 100 : Math.min(100, Math.round((el.scrollTop / max) * 100)))
    }
    on()
    window.addEventListener('scroll', on, { passive: true })
    return () => window.removeEventListener('scroll', on)
  }, [data])
  if (error) return <ErrorBox error={error} />
  if (!data) return <Loader label="Unrolling the scroll…" />
  const markRead = async () => {
    setBusy(true)
    setErr(undefined)
    try {
      await api(`${path}/read`, { method: 'POST' })
      nav(`/p/${team}/${training}`)
    } catch (e) {
      setErr((e as Error).message)
      setBusy(false)
    }
  }
  return (
    <article className="page">
      <div className="reading-progress"><MoltenBar percent={depth} label="Reading progress" caption={`${depth}% read`} /></div>
      <Link to={`/p/${team}/${training}`}>← Back to the training</Link>
      <Markdown text={data.markdown} assetBase={`/api/programs/${team}/${training}/assets`} />
      {err && <p className="error" role="alert">{err}</p>}
      <button className="primary" disabled={busy} onClick={markRead}>Mark as read</button>
    </article>
  )
}
