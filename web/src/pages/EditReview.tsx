import { useState } from 'react'
import { Link, useParams } from 'react-router'
import { api, ApiError } from '../api'
import { useMe } from '../me'
import { useFetch } from '../useFetch'
import type { ContentEdit, DraftInfo } from '../types'
import { DiffView } from '../components/DiffView'
import { SmithMark } from '../components/SmithMark'
import { ErrorBox } from '../components/ErrorBox'
import { Loader } from '../components/Loader'
import { Markdown } from '../components/Markdown'
import { conflictNotice } from '../lib/conflictNotice'
import { byteLen } from '../lib/editDraft'

export function EditReviewPage() {
  const { id } = useParams()
  const { me } = useMe()
  const { data: e, error, reload } = useFetch<ContentEdit>(`/api/edits/${id}`)
  const drafts = useFetch<DraftInfo[]>(me.can_edit_content ? '/api/authoring/drafts' : null)
  const [note, setNote] = useState('')
  const [msg, setMsg] = useState('')
  const [busy, setBusy] = useState(false)
  if (error) return <ErrorBox error={error} />
  if (!e) return <Loader label="Reading the edit…" />
  const draft = drafts.data?.find((d) => d.edit_id === e.id) // the draft it came from, still as the author left it

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
        <span className="muted">{e.training} · by {e.author}</span> <SmithMark smith={e.author_smith} />
        {e.reviewer && <><span className="muted"> · reviewed by {e.reviewer}</span> <SmithMark smith={e.reviewer_smith} /></>}
      </p>
      {e.status === 'stale' && <p role="alert" className="error">This edit no longer applies to the current content.</p>}
      {e.note && <p className="hint"><span className="muted">Review note from {e.reviewer}:</span> {e.note}</p>}
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
        <form className="stack panel" aria-labelledby="decide" onSubmit={(x) => { x.preventDefault(); void act('approve') }}>
          <h2 id="decide">{e.can_review ? 'Your review' : 'Waiting for review'}</h2>
          <p className="muted">
            {e.can_review
              ? 'Approving merges exactly this change, and trainees see it at once. Rejecting leaves the training as it is. The author reads your note either way.'
              : 'A maintainer or an admin reviews it. Withdraw it to keep working on the draft.'}
          </p>
          {e.can_review && (
            <label>Review note <textarea rows={4} placeholder="What you checked, or what to change" value={note} onChange={(x) => byteLen(x.target.value) <= 2000 && setNote(x.target.value)} /></label>
          )}
          <div className="row">
            {e.can_review && <>
              <button className="primary" disabled={busy}>Approve and merge</button>
              <button type="button" className="ghost" disabled={busy} onClick={() => act('reject')}>Reject</button>
            </>}
            {e.can_withdraw && <button type="button" className="ghost" disabled={busy} onClick={() => act('withdraw')}>Withdraw</button>}
          </div>
        </form>
      )}
      <p role="alert" className="error">{msg}</p>
      {['stale', 'rejected', 'withdrawn'].includes(e.status) && me.can_edit_content && e.author === me.user.email.toLowerCase() && (
        <Link className="button-link" to={draft ? `/edits/drafts/${draft.id}` : `/edits/new?training=${encodeURIComponent(e.training)}&from=${e.id}`}>Reopen in the editor</Link>
      )}
    </section>
  )
}
