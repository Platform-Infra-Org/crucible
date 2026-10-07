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
import { conflictNotice } from '../lib/conflictNotice'
import { byteLen } from '../lib/editDraft'

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
      <p role="alert" className="error">{msg}</p>
      <h2>Changes</h2>
      <DiffView diff={e.diff ?? ''} />
      <h2>Files</h2>
      {(e.ops ?? []).map((op, i) =>
        op.op === 'put' ? (
          <details key={i}>
            <summary>{op.path}</summary>
            {op.path.endsWith('.md') ? <Markdown text={op.content} /> : <pre>{op.content}</pre>}
          </details>
        ) : op.op === 'rename' ? <p key={i}>Renamed <code>{op.from}</code> to <code>{op.to}</code></p>
          : <p key={i}>Deleted <code>{op.path}</code> (its full text is in the changes above)</p>,
      )}
      {(e.can_review || e.can_withdraw) && (
        <p>
          {e.can_review && <><label>Review note <textarea value={note} onChange={(x) => byteLen(x.target.value) <= 2000 && setNote(x.target.value)} /></label>{' '}</>}
          {e.can_review && <><button disabled={busy} onClick={() => act('approve')}>Approve and merge</button>{' '}
            <button className="ghost" disabled={busy} onClick={() => act('reject')}>Reject</button>{' '}</>}
          {e.can_withdraw && <button className="ghost" disabled={busy} onClick={() => act('withdraw')}>Withdraw</button>}
        </p>
      )}
      {['stale', 'rejected', 'withdrawn'].includes(e.status) && me.can_edit_content && e.author === me.user.email.toLowerCase() && (
        <Link to={`/edits/new?training=${encodeURIComponent(e.training)}&from=${e.id}`}>Reopen in the editor</Link>
      )}
    </section>
  )
}
