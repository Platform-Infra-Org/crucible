import { Link } from 'react-router'
import { api } from '../api'
import { useFetch } from '../useFetch'
import type { KillSwitch, PlatformView } from '../types'
import { ErrorBox } from '../components/ErrorBox'
import { Loader } from '../components/Loader'
import { toast } from '../lib/alerts'
import { usd } from '../lib/money'

export function ForgeStatusPage() {
  const plat = useFetch<PlatformView>('/api/admin/platform')
  const ks = useFetch<KillSwitch>('/api/kill-switch')
  const err = plat.error ?? ks.error
  if (err) return <ErrorBox error={err} />
  if (!plat.data || !ks.data) return <Loader label="Reading the gauges…" />
  const p = plat.data
  const k = ks.data
  const toggle = async () => {
    const enable = !k.enabled
    if (!confirm(enable ? 'Destroy every running lab and block new requests until you resume?' : 'Let labs run again?')) return
    try {
      await api('/api/admin/kill-switch', { method: 'POST', json: { enabled: enable } })
      ks.reload()
      plat.reload()
    } catch (e) {
      toast((e as Error).message)
    }
  }
  return (
    <section className="page">
      <h1>Forge Status</h1>
      <h2>Lab kill switch</h2>
      <p role="status" data-testid="kill-switch-status" className={k.enabled ? 'error' : 'pass'}>
        {k.enabled ? `Labs are paused (by ${k.changed_by}, ${new Date(k.changed_at ?? '').toLocaleString()}).` : 'Labs are running.'}
      </p>
      <button className={k.enabled ? 'primary' : 'danger'} onClick={toggle}>{k.enabled ? 'Resume labs' : 'Pause all labs'}</button>

      <h2>Needs attention</h2>
      {p.pending_edits > 0 && <p><Link to="/edits">{p.pending_edits} content edit{p.pending_edits === 1 ? '' : 's'} waiting for review</Link></p>}
      {p.attention.length === 0 ? <p className="pass">No failed or stuck labs.</p> : (
        <table className="grid">
          <thead><tr><th>Lab</th><th>Trainee</th><th>Program</th><th>State</th><th>Since</th><th>Error</th></tr></thead>
          <tbody>{p.attention.map((a) => (
            <tr key={a.id}><td><code>{a.id}</code></td><td>{a.trainee}</td><td>{a.team}/{a.training} · {a.module}</td>
              <td className={a.state === 'failed' ? 'error' : 'warn'}>{a.state}</td><td>{new Date(a.since).toLocaleString()}</td><td>{a.error}</td></tr>
          ))}</tbody>
        </table>
      )}
      <h2>Program versions</h2>
      <table className="grid">
        <thead><tr><th>Program</th><th>Runs</th><th>Branch head</th></tr></thead>
        <tbody>{p.programs.map((g) => (
          <tr key={g.team + g.training}><td>{g.team}/{g.training}</td><td><code>{g.running.slice(0, 7)}</code>{g.pinned_ref && <span className="badge"> pinned</span>}</td>
            <td><code>{g.head.slice(0, 7)}</code>{g.running !== g.head && <span className="badge warn"> behind</span>}</td></tr>
        ))}</tbody>
      </table>

      <h2>Sync</h2>
      <dl className="facts">
        <dt>Platform commit</dt><dd><code>{p.platform_sha.slice(0, 12)}</code></dd>
        <dt>Last sync</dt><dd>{new Date(p.synced_at).toLocaleString()}</dd>
        {p.platform_error && (<><dt>Platform error</dt><dd className="error">{p.platform_error}</dd></>)}
      </dl>
      <table className="grid">
        <thead><tr><th>Training</th><th>Repo</th><th>Head</th><th>Problems</th></tr></thead>
        <tbody>
          {p.trainings.map((t) => (
            <tr key={t.id}>
              <td>{t.id}</td><td><code>{t.repo}</code> ({t.branch})</td><td><code>{t.head.slice(0, 7)}</code></td>
              <td>{t.problems.length === 0 ? '✓' : <ul>{t.problems.map((m) => <li key={m} className="error">{m}</li>)}</ul>}</td>
            </tr>
          ))}
        </tbody>
      </table>

      <h2>Platform settings</h2>
      <p className="muted">These live in the platform repo (platform.yaml, admins.yaml). Change them there; Crucible picks them up on the next sync.</p>
      <dl className="facts">
        <dt>Cost tiers</dt>
        <dd>{p.cost_tiers ? `auto ≤ ${usd(p.cost_tiers.auto_approve_usd)} · approver ≤ ${usd(p.cost_tiers.tier1_usd)} · leader ≤ ${usd(p.cost_tiers.tier2_usd)} · admin above` : 'not set'}</dd>
        <dt>Escalation</dt><dd>after {p.escalation_hours} business hours per tier</dd>
        <dt>Schedules</dt><dd>{Object.entries(p.schedules).map(([n, d]) => `${n}: ${d}`).join(' · ') || 'none'}</dd>
        <dt>Admins</dt><dd>{p.admins.join(', ')}</dd>
      </dl>

      <h2>Recent privileged actions</h2>
      <table className="grid">
        <thead><tr><th>When</th><th>Who</th><th>Action</th><th>Target</th><th>Commit</th></tr></thead>
        <tbody>
          {p.audit.map((e, i) => (
            <tr key={i}>
              <td>{new Date(e.at).toLocaleString()}</td><td>{e.actor}</td><td>{e.action}</td><td>{e.target}</td>
              <td>{e.commit_sha ? <code>{e.commit_sha.slice(0, 7)}</code> : ''}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </section>
  )
}
