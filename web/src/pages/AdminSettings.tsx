import { useState, type FormEvent } from 'react'
import { api } from '../api'
import { useFetch } from '../useFetch'
import type { AdminSettings, PlatformView } from '../types'
import { ErrorBox } from '../components/ErrorBox'
import { Loader } from '../components/Loader'
import { Conflict } from '../components/Conflict'
import { tiersRequest, windowsRequest } from '../lib/adminConfig'
import { THEMES } from '../theme/theme'
import { ApiError } from '../api'

const RELOAD = 'Someone changed this, reload to see the latest.'

// useSave runs one save and reports the result in a status line; a 409 turns into the reload offer.
function useSave(reload: () => void) {
  const [status, setStatus] = useState('')
  const [bad, setBad] = useState(false)
  const [stale, setStale] = useState(false)
  const [busy, setBusy] = useState(false)
  const run = async (fn: () => Promise<void>, ok: string) => {
    if (busy) return
    setBusy(true)
    setStale(false)
    try {
      await fn()
      setBad(false)
      setStatus(ok)
      reload()
    } catch (e) {
      setBad(true)
      if (e instanceof ApiError && e.status === 409) { setStale(true); setStatus('') } else setStatus(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }
  const fail = (msg: string) => { setBad(true); setStale(false); setStatus(msg) }
  const note = (
    <>
      {stale && <Conflict onReload={() => { setStale(false); reload() }} message={RELOAD} />}
      <p role="status" className={bad ? 'error' : 'pass'}>{status}</p>
    </>
  )
  return { run, fail, note, busy }
}

export function AdminSettingsPage() {
  const s = useFetch<AdminSettings>('/api/admin/settings')
  const admins = useFetch<{ admins: string[] }>('/api/admin/admins')
  const plat = useFetch<PlatformView>('/api/admin/platform')
  const meta = useFetch<{ quotes: string[] }>('/api/meta')
  const err = s.error ?? admins.error ?? plat.error ?? meta.error
  if (err) return <ErrorBox error={err} />
  if (!s.data || !admins.data || !plat.data || !meta.data) return <Loader label="Reading the gauges…" />
  const reloadAll = () => { s.reload(); admins.reload(); plat.reload(); meta.reload() }
  return (
    <section className="page">
      <h1>Forge settings</h1>
      {!s.data.cost_tiers && (
        <p role="alert" className="warn">No cost tiers are set, so lab requests that cost money can't be approved yet. Set the three lines under Lab spending below.</p>
      )}
      {admins.data.admins.length === 1 && (
        <p role="alert" className="warn">{admins.data.admins[0]} is the only admin. Add a second admin: losing this account locks the forge, and the last admin can't be removed.</p>
      )}
      <Main key={s.data.version} s={s.data} reload={reloadAll} />
      <Admins list={admins.data.admins} reload={reloadAll} />
      <Schedules map={plat.data.schedules} reload={reloadAll} />
      <Quotes key={meta.data.quotes.join('\n')} quotes={meta.data.quotes} reload={reloadAll} />
    </section>
  )
}

function Main({ s, reload }: { s: AdminSettings; reload: () => void }) {
  const t = s.cost_tiers
  const [theme, setTheme] = useState(s.default_theme)
  const [auto, setAuto] = useState(t ? String(t.auto_approve_usd) : '')
  const [t1, setT1] = useState(t ? String(t.tier1_usd) : '')
  const [t2, setT2] = useState(t ? String(t.tier2_usd) : '')
  const [rate, setRate] = useState(s.cluster_usd_per_hour === null ? '' : String(s.cluster_usd_per_hour))
  const [esc, setEsc] = useState(String(s.escalation_hours))
  const [ranks, setRanks] = useState(() => Object.fromEntries(Object.entries(s.ranks).map(([k, v]) => [k, String(v)])))
  const { run, fail, note, busy } = useSave(reload)
  const save = (e: FormEvent) => {
    e.preventDefault()
    const r = tiersRequest(auto, t1, t2)
    if (r.problem) return fail(r.problem)
    void run(async () => {
      await api('/api/admin/settings', { method: 'PUT', json: {
        version: s.version, default_theme: theme, cost_tiers: r.tiers,
        cluster_usd_per_hour: rate.trim() === '' ? null : Number(rate), escalation_hours: Number(esc) || 0,
        ranks: Object.fromEntries(Object.entries(ranks).map(([k, v]) => [k, Number(v)])) } })
    }, 'Settings saved.')
  }
  return (
    <form className="stack" onSubmit={save}>
      <h2>Look</h2>
      <label>Default theme
        <select value={theme} onChange={(e) => setTheme(e.target.value)}>
          {THEMES.map((x) => <option key={x.id} value={x.id}>{x.name}</option>)}
        </select>
      </label>
      <h2>Lab spending</h2>
      <p className="muted">Lab requests under the first line are approved on their own. Above it they go to the program's approver; above the second line they need an admin. Clear all three to leave them unset.</p>
      <label>Auto-approve under (USD) <input type="number" min={0} step="0.01" value={auto} onChange={(e) => setAuto(e.target.value)} /></label>
      <label>Program approver up to (USD) <input type="number" min={0} step="0.01" value={t1} onChange={(e) => setT1(e.target.value)} /></label>
      <label>Admin approval above (USD) <input type="number" min={0} step="0.01" value={t2} onChange={(e) => setT2(e.target.value)} /></label>
      <label>Cluster lab rate (USD per hour, empty = cluster labs unavailable) <input type="number" min={0} step="0.01" value={rate} onChange={(e) => setRate(e.target.value)} /></label>
      <label>Hours before a waiting request escalates <input type="number" min={0} step="0.5" value={esc} onChange={(e) => setEsc(e.target.value)} /></label>
      <h2>Forge ranks</h2>
      <p className="muted">The percent of a training a trainee must complete to reach each rank.</p>
      {Object.keys(ranks).map((k) => (
        <label key={k}>{k[0].toUpperCase() + k.slice(1)} at (%) <input type="number" min={0} max={100} value={ranks[k]} onChange={(e) => setRanks({ ...ranks, [k]: e.target.value })} /></label>
      ))}
      <button className="primary" disabled={busy}>Save settings</button>
      {note}
    </form>
  )
}

function Admins({ list, reload }: { list: string[]; reload: () => void }) {
  const [email, setEmail] = useState('')
  const { run, note, busy } = useSave(reload)
  const only = list.length === 1
  return (
    <div className="stack">
      <h2>Admins</h2>
      <ul>
        {list.map((a) => (
          <li key={a}>{a}{' '}
            <button type="button" className="ghost" disabled={busy || only} title={only ? "The last admin can't be removed" : undefined}
              onClick={() => window.confirm(`Remove ${a} as an admin?`) && run(() => api(`/api/admin/admins/${encodeURIComponent(a)}`, { method: 'DELETE' }), `${a} is no longer an admin.`)}>Remove</button>
          </li>
        ))}
      </ul>
      <form className="row" onSubmit={(e) => { e.preventDefault(); void run(() => api('/api/admin/admins', { method: 'POST', json: { email } }).then(() => setEmail('')), `${email} is now an admin.`) }}>
        <label>New admin email <input type="email" required value={email} onChange={(e) => setEmail(e.target.value)} /></label>
        <button className="primary" disabled={busy}>Add admin</button>
      </form>
      {note}
    </div>
  )
}

function Schedules({ map, reload }: { map: Record<string, string>; reload: () => void }) {
  const [name, setName] = useState('')
  const [tz, setTz] = useState('UTC')
  const [lines, setLines] = useState('mon,tue,wed,thu,fri 08:00-19:00')
  const { run, fail, note, busy } = useSave(reload)
  const add = (e: FormEvent) => {
    e.preventDefault()
    const w = windowsRequest(lines)
    if (w.problem) return fail(w.problem)
    void run(() => api(`/api/admin/schedules/${encodeURIComponent(name.trim())}`, { method: 'PUT', json: { timezone: tz.trim(), windows: w.windows } }), `Schedule ${name.trim()} saved.`)
  }
  const names = Object.keys(map).sort()
  return (
    <form className="stack" onSubmit={add}>
      <h2>Lab schedules</h2>
      <p className="muted">A schedule says when a program's labs may run. Saving under an existing name replaces it.</p>
      {names.length === 0 && <p className="muted">No schedules yet; programs can run labs any time.</p>}
      <ul>
        {names.map((n) => (
          <li key={n}><strong>{n}</strong>: {map[n]}{' '}
            <button type="button" className="ghost" disabled={busy}
              onClick={() => window.confirm(`Delete schedule ${n}?`) && run(() => api(`/api/admin/schedules/${encodeURIComponent(n)}`, { method: 'DELETE' }), `Schedule ${n} deleted.`)}>Delete</button>
          </li>
        ))}
      </ul>
      <label>Schedule name <input required value={name} onChange={(e) => setName(e.target.value)} /></label>
      <label>Time zone (e.g. Europe/Berlin) <input required value={tz} onChange={(e) => setTz(e.target.value)} /></label>
      <label>Open windows, one per line, like “mon,tue 08:00-19:00” <textarea rows={3} value={lines} onChange={(e) => setLines(e.target.value)} /></label>
      <button className="primary" disabled={busy}>Save schedule</button>
      {note}
    </form>
  )
}

function Quotes({ quotes, reload }: { quotes: string[]; reload: () => void }) {
  const [text, setText] = useState(quotes.join('\n'))
  const { run, note, busy } = useSave(reload)
  return (
    <form className="stack" onSubmit={(e) => { e.preventDefault(); void run(() => api('/api/admin/quotes', { method: 'PUT', json: { quotes: text.split('\n').map((q) => q.trim()).filter(Boolean) } }), 'Quotes saved.') }}>
      <h2>Loading quotes</h2>
      <p className="muted">One line per quote, shown while the forge loads.</p>
      <label>Quotes <textarea rows={5} value={text} onChange={(e) => setText(e.target.value)} /></label>
      <button className="primary" disabled={busy}>Save quotes</button>
      {note}
    </form>
  )
}
