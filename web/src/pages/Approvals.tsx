import { useState } from 'react'
import { api } from '../api'
import { useFetch } from '../useFetch'
import type { Approval, Spend, Tier } from '../types'
import { ErrorBox } from '../components/ErrorBox'
import { Loader } from '../components/Loader'
import { toast } from '../lib/alerts'
import { hours, usd } from '../lib/money'

export const tierLabel: Record<Tier, string> = { auto: 'auto-approved', approver: 'a program approver', leader: 'the team leader', admin: 'an admin' }

export function ApprovalsPage() {
  const { data, error, reload } = useFetch<Approval[]>('/api/approvals', 15_000)
  if (error) return <ErrorBox error={error} />
  if (!data) return <Loader label="Checking the queue…" />
  return (
    <section className="page">
      <h1>Approvals</h1>
      {data.length === 0 && <p className="muted">Nothing waiting. The forge is quiet.</p>}
      <ul className="approvals">
        {data.map((a) => <ApprovalCard key={a.kind + a.id} a={a} onDone={reload} />)}
      </ul>
    </section>
  )
}

function SpendLine({ s }: { s: Spend }) {
  if (!s.budget_usd) return <>{usd(s.spent_usd)} spent · no budget set</>
  return (
    <>
      <meter min={0} max={s.cap_usd || s.budget_usd} low={0.8 * s.budget_usd} high={s.budget_usd} value={s.spent_usd} /> {usd(s.spent_usd)} of{' '}
      {usd(s.budget_usd)}{s.cap_usd > s.budget_usd ? ` (cap ${usd(s.cap_usd)})` : ''} · {usd(s.committed_usd)} committed
    </>
  )
}

function ApprovalCard({ a, onDone }: { a: Approval; onDone: () => void }) {
  const [note, setNote] = useState('')
  const [busy, setBusy] = useState(false)
  const ext = a.kind === 'extension'
  const decide = async (approve: boolean) => {
    setBusy(true)
    try {
      await api(`/api/approvals/${a.id}${ext ? '/extension' : ''}`, { method: 'POST', json: { approve, note } })
      toast(approve ? (ext ? 'Approved: the lab runs longer.' : 'Approved: the lab is starting.') : 'Rejected.')
      onDone()
    } catch (e) {
      toast((e as Error).message)
      onDone() // ponytail: a 409 means the request moved on (decided elsewhere, or passed to an admin); refresh the queue
    } finally {
      setBusy(false)
    }
  }
  return (
    <li className="card approval" aria-label={`${ext ? 'Extension' : 'Request'} from ${a.requester}`}>
      <h2>{ext && 'Extension: '}{a.lab_title} <small className="muted">{a.team} / {a.training}</small></h2>
      <p>{a.requester_name || a.requester} <span className="muted">{a.requester}</span> · requested {new Date(a.requested_at).toLocaleString()}</p>
      <dl className="facts">
        {ext && a.extend_until && <><dt>Until</dt><dd>{new Date(a.extend_until).toLocaleString()}</dd></>}
        <dt>Estimate</dt>
        <dd data-testid="estimate">
          {usd(a.estimate_usd)} {ext ? `(${usd(a.hourly_usd)}/h, the whole lab with the extension, ${a.runtime})` : `(${usd(a.hourly_usd)}/h × ${hours(a.ttl_s)}, ${a.runtime})`}
        </dd>
        <dt>Waiting for</dt>
        <dd>
          {tierLabel[a.tier]}
          {a.escalate_at && <span className="muted"> · escalates {new Date(a.escalate_at).toLocaleString()}</span>}
          {a.over_cap && <strong className="warn"> · would pass a budget cap: approving is an audited admin override</strong>}
        </dd>
        <dt>Team this month</dt><dd><SpendLine s={a.team_spend} /></dd>
        <dt>Program this month</dt><dd><SpendLine s={a.program_spend} /></dd>
        <dt>Schedule</dt><dd>{a.schedule.text}{!a.schedule.open && ' · closed now'}</dd>
        <dt>Recent labs</dt>
        <dd>{a.recent.length === 0 ? 'none' : a.recent.map((r) => `${r.module} (${r.state}${r.end_reason ? `, ${r.end_reason}` : ''})`).join(' · ')}</dd>
      </dl>
      <div className="row">
        <label>Note <input value={note} maxLength={500} onChange={(e) => setNote(e.target.value)} /></label>
        <button className="primary" disabled={busy} onClick={() => decide(true)}>Approve</button>
        <button className="ghost" disabled={busy} onClick={() => decide(false)}>Reject</button>
      </div>
    </li>
  )
}
