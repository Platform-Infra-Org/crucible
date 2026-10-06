import { useState } from 'react'
import { api } from '../api'
import { useFetch } from '../useFetch'
import type { Ledger, LedgerLab, Spend } from '../types'
import { ErrorBox } from '../components/ErrorBox'
import { Loader } from '../components/Loader'
import { toast } from '../lib/alerts'
import { usd } from '../lib/money'
import { accuracyPct, barHeights, limitOf } from '../lib/ledger'

function Burn({ label, s }: { label: string; s: Spend }) {
  const limit = limitOf(s)
  return (
    <div className="burn">
      <span>{label}</span>
      {limit > 0 ? <meter aria-label={`${label} spend`} min={0} max={limit} low={limit * 0.8} high={limit} optimum={0} value={s.spent_usd} /> : <span />}
      <span className="muted">
        {usd(s.spent_usd)}{limit > 0 && ` of ${usd(limit)}`} · committed {usd(s.committed_usd)}
        {s.actual_usd > 0 && ` · ${usd(s.actual_usd)} from settled AWS bills`}
      </span>
    </div>
  )
}

function LabRows({ labs, testid }: { labs: LedgerLab[]; testid: string }) {
  return (
    <table className="grid" data-testid={testid}>
      <thead><tr><th>Lab</th><th>Team</th><th>Requested by</th><th>Runtime</th><th>State</th><th>Estimate</th><th>Cost so far</th><th>AWS bill</th></tr></thead>
      <tbody>
        {labs.map((l) => (
          <tr key={l.id}>
            <td>{l.training} / {l.module}</td><td>{l.team}</td><td>{l.requester}</td><td>{l.runtime}</td>
            <td>{l.state}{l.state === 'ready' && l.ends_at ? ` · ends ${new Date(l.ends_at).toLocaleTimeString()}` : ''}</td>
            <td>{usd(l.estimate_usd)}</td><td>{usd(l.cost_usd)}</td>
            <td>{l.actual_usd == null ? '—' : `${usd(l.actual_usd)}${l.settled ? '' : ' (so far)'}`}</td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}

export function LedgerPage() {
  const { data: l, error, reload } = useFetch<Ledger>('/api/ledger', 60_000)
  const [busy, setBusy] = useState(false)
  if (error) return <ErrorBox error={error} />
  if (!l) return <Loader label="Weighing the ledger…" />
  const refresh = async () => {
    setBusy(true)
    try {
      await api('/api/admin/finops/refresh', { method: 'POST' })
      reload()
    } catch (e) {
      toast((e as Error).message) // 409 when rate-limited: the server's message
    } finally {
      setBusy(false)
    }
  }
  const bars = barHeights(l.daily)
  const acc = accuracyPct(l.accuracy)
  return (
    <section className="page">
      <h1>Ledger</h1>
      <p className="muted">
        Lab spend for {l.month}. Running labs count from their hourly estimate. A finished AWS lab switches to its AWS bill
        once the bill has settled (about two days after the lab ends).
      </p>
      {l.aws && (
        <p role="status" data-testid="actuals-status" className={l.actuals_stale ? 'error' : 'muted'}>
          {l.actuals_as_of ? `AWS costs as of ${new Date(l.actuals_as_of).toLocaleString()} (AWS reports about 24 h late).` : 'No AWS costs have been read yet.'}
          {l.actuals_stale && ' STALE: actuals are out of date; estimates keep enforcing the budgets.'}
          {l.actuals_error && ` Last error: ${l.actuals_error}`}
        </p>
      )}
      {l.aws && (l.reaper_stale || l.reaper_error) && (
        <p role="alert" data-testid="reaper-status" className="error">
          The reaper has not run successfully lately, so stray cloud resources may be going unnoticed.
          {l.reaper_error && ` Last error: ${l.reaper_error}`}
        </p>
      )}
      {l.can_refresh && <button onClick={refresh} disabled={busy}>{busy ? 'Refreshing…' : 'Refresh now'}</button>}

      <h2>Budgets</h2>
      {l.teams.map((t) => (
        <div key={t.id} className="card">
          <Burn label={t.name} s={t.spend} />
          {t.programs.map((p) => <Burn key={p.training} label={`${t.name} / ${p.training}`} s={p.spend} />)}
        </div>
      ))}

      <h2>Day by day</h2>
      <p className="muted legend"><span className="swatch est" /> estimate (by start day) <span className="swatch act" /> AWS bill</p>
      <div className="bars" role="img" aria-label="Daily lab spend this month; the numbers are in the table below">
        {l.daily.map((d, i) => (
          <div key={d.day} className="bar-day" title={`${d.day}: estimate ${usd(d.estimate_usd)}, AWS ${usd(d.actual_usd)}`}>
            <span className="bar est" style={{ height: `${bars[i].estimate}%` }} />
            <span className="bar act" style={{ height: `${bars[i].actual}%` }} />
          </div>
        ))}
      </div>
      <details>
        <summary>Numbers</summary>
        <table className="grid">
          <thead><tr><th>Day</th><th>Estimate</th><th>AWS bill</th></tr></thead>
          <tbody>{l.daily.map((d) => <tr key={d.day}><td>{d.day}</td><td>{usd(d.estimate_usd)}</td><td>{usd(d.actual_usd)}</td></tr>)}</tbody>
        </table>
      </details>

      <h2>Running now</h2>
      {l.running.length === 0 ? <p className="muted">No labs are running.</p> : <LabRows labs={l.running} testid="ledger-running" />}

      <h2>Labs this month</h2>
      {l.labs.length === 0 ? <p className="muted">No lab has run this month.</p> : <LabRows labs={l.labs} testid="ledger-labs" />}

      <h2>Top spenders</h2>
      {l.top_spenders.length === 0 ? <p className="muted">Nothing spent yet.</p> : (
        <table className="grid">
          <thead><tr><th>Requested by</th><th>Team</th><th>This month</th></tr></thead>
          <tbody>{l.top_spenders.map((s) => <tr key={s.requester + s.team}><td>{s.requester}</td><td>{s.team}</td><td>{usd(s.usd)}</td></tr>)}</tbody>
        </table>
      )}

      <h2>Estimate vs actual</h2>
      <p>{acc === null ? 'No finished AWS lab has a settled bill yet.'
        : `${l.accuracy.labs} settled lab(s): AWS billed ${usd(l.accuracy.actual_usd)} against ${usd(l.accuracy.estimate_usd)} estimated (${acc}%).`}</p>

      {l.can_refresh && (
        <>
          <h2>Reaper findings</h2>
          <p className="muted">{l.reaped_at ? `Last reaper run: ${new Date(l.reaped_at).toLocaleString()}.` : 'The reaper has not run yet.'}</p>
          <table className="grid" data-testid="reaper-findings">
            <thead><tr><th>Last seen</th><th>Found by</th><th>Lab</th><th>Resource</th><th>Action</th><th>Detail</th></tr></thead>
            <tbody>
              {(l.findings ?? []).map((f) => (
                <tr key={f.source + f.lab_id + f.arn}>
                  <td>{new Date(f.last_at).toLocaleString()}</td><td>{{ destroy: 'end-of-lab sweep', reaper: 'reaper', trail: 'CloudTrail' }[f.source]}</td>
                  <td>{f.lab_id}</td><td><code>{f.arn}</code></td><td className={f.action === 'deleted' ? 'pass' : 'error'}>{f.action}</td><td>{f.detail}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </>
      )}
    </section>
  )
}
