import { useState } from 'react'
import { Link, useParams } from 'react-router'
import { api, ApiError } from '../api'
import { useMe } from '../me'
import { useFetch } from '../useFetch'
import type { ContentEdit } from '../types'
import { DiffView } from '../components/DiffView'
import { ErrorBox } from '../components/ErrorBox'
import { Loader } from '../components/Loader'
import { Markdown } from '../components/Markdown'

// The server answers 409 for two different things; say which one happened.
export function conflictNotice(msg: string): string {
  if (/changed since it was reviewed/.test(msg)) return `Edit moved: ${msg} Nothing was merged; read the new diff below and approve again.`
  if (/no longer applies/.test(msg)) return `Needs re-approval: ${msg}`
  return msg
}

export function EditReviewPage() {
  const { id } = useParams()
  const { me } = useMe()
  const { data: e, error, reload } = useFetch<ContentEdit>(`/api/edits/${id}`)
  const [note, setNote] = useState('')
  const [msg, setMsg] = useState('')
  const [busy, setBusy] = useState(false)
  if (error) return <ErrorBox error={error} />
  if (!e) return <Loader label="Reading the edit…" />

  const act = async (action: 'approve' | 'reject' | 'withdraw') => {
    setBusy(true)
    setMsg('')
    try {
      await api(`/api/edits/${id}/${action}`, { method: 'POST', json: action === 'withdraw' ? {} : { note } })
    } catch (x) {
      const er = x as ApiError
      setMsg(er.status === 409 ? conflictNotice(er.message) : er.message)
    } finally {
      setBusy(false)
      reload()
    }
  }
  return (
    <section className="page">
      <h1>{e.title}</h1>
      <p>
        <span className={`badge ${e.status}`} data-testid="edit-status">{e.status}</span>{' '}
        <span className="muted">{e.training} · by {e.author}{e.reviewer ? ` · reviewed by ${e.reviewer}` : ''}</span>
      </p>
      {e.status === 'stale' && <p role="alert" className="error">This edit no longer applies to the current content.</p>}
      {e.note && <p>Note: {e.note}</p>}
      {msg && <p role="alert" className="error">{msg}</p>}
      <h2>Changes</h2>
      <DiffView diff={e.diff ?? ''} />
      <h2>Files</h2>
      {Object.entries(e.files ?? {}).map(([p, t]) => (
        <details key={p}>
          <summary>{p}</summary>
          {p.endsWith('.md') ? <Markdown text={t} /> : <pre>{t}</pre>}
        </details>
      ))}
      {(e.can_review || e.can_withdraw) && (
        <p>
          {e.can_review && <><label>Review note <textarea value={note} maxLength={2000} onChange={(x) => setNote(x.target.value)} /></label>{' '}</>}
          {e.can_review && <><button disabled={busy} onClick={() => act('approve')}>Approve and merge</button>{' '}
            <button className="ghost" disabled={busy} onClick={() => act('reject')}>Reject</button>{' '}</>}
          {e.can_withdraw && <button className="ghost" disabled={busy} onClick={() => act('withdraw')}>Withdraw</button>}
        </p>
      )}
      {e.status === 'stale' && e.author === me.user.email.toLowerCase() && (
        <Link to={`/edits/new?training=${encodeURIComponent(e.training)}&from=${e.id}`}>Redo on the current version</Link>
      )}
    </section>
  )
}
