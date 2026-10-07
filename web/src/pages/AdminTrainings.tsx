import { useState, type FormEvent } from 'react'
import { api } from '../api'
import { useFetch } from '../useFetch'
import type { PlatformView, RegisteredTraining } from '../types'
import { ErrorBox } from '../components/ErrorBox'
import { Loader } from '../components/Loader'
import { Conflict } from '../components/Conflict'
import { ApiError } from '../api'
import { repoWarning } from '../lib/adminConfig'

export function AdminTrainingsPage() {
  const t = useFetch<{ trainings: RegisteredTraining[] }>('/api/admin/trainings')
  const plat = useFetch<PlatformView>('/api/admin/platform')
  const [id, setId] = useState('')
  const [repo, setRepo] = useState('')
  const [branch, setBranch] = useState('main')
  const [status, setStatus] = useState('')
  const [bad, setBad] = useState(false)
  const [stale, setStale] = useState(false)
  const [busy, setBusy] = useState(false)
  const err = t.error ?? plat.error
  if (err) return <ErrorBox error={err} />
  if (!t.data || !plat.data) return <Loader label="Counting the forge's moulds…" />
  const reload = () => { t.reload(); plat.reload() }
  const existing = t.data.trainings.find((x) => x.id === id.trim())
  const warn = repoWarning(existing?.repo, repo, plat.data.programs.filter((p) => p.training === id.trim()).length)
  const act = async (fn: () => Promise<unknown>, ok: string) => {
    if (busy) return
    setBusy(true)
    setStale(false)
    try { await fn(); setBad(false); setStatus(ok); reload() } catch (e) {
      setBad(true)
      if (e instanceof ApiError && e.status === 409) { setStale(true); setStatus('') } else setStatus(e instanceof Error ? e.message : String(e))
    } finally { setBusy(false) }
  }
  const save = (e: FormEvent) => {
    e.preventDefault()
    if (warn && !window.confirm(warn)) return
    void act(() => api('/api/admin/trainings', { method: 'POST', json: { id: id.trim(), repo: repo.trim(), branch: branch.trim() } }), `${id.trim()} is ${existing ? 'updated' : 'registered'}.`)
  }
  return (
    <section className="page">
      <h1>Trainings</h1>
      <p className="muted">The git repositories Crucible reads trainings from. Registering an id that already exists points it at the new repository or branch.</p>
      {t.data.trainings.length === 0 ? <p className="muted">No trainings are registered yet.</p> : (
        <div className="table-wrap"><table className="grid">
          <thead><tr><th>Training</th><th>Repository</th><th>Branch</th><th /></tr></thead>
          <tbody>{t.data.trainings.map((x) => (
            <tr key={x.id}><td>{x.id}</td><td><code>{x.repo}</code></td><td>{x.branch}</td>
              <td>
                <button type="button" className="ghost" onClick={() => { setId(x.id); setRepo(x.repo); setBranch(x.branch) }}>Edit</button>{' '}
                <button type="button" className="ghost" disabled={busy}
                  onClick={() => window.confirm(`Unregister ${x.id}? Programs using it will stop finding its content.`) && act(() => api(`/api/admin/trainings/${encodeURIComponent(x.id)}`, { method: 'DELETE' }), `${x.id} is unregistered.`)}>Unregister</button>
              </td></tr>
          ))}</tbody>
        </table></div>
      )}
      <form className="stack" onSubmit={save}>
        <h2>{existing ? `Repoint ${existing.id}` : 'Register a training'}</h2>
        <label>Training id <input required value={id} onChange={(e) => setId(e.target.value)} /></label>
        <label>Repository URL <input required value={repo} onChange={(e) => setRepo(e.target.value)} /></label>
        <label>Branch <input required value={branch} onChange={(e) => setBranch(e.target.value)} /></label>
        {warn && <p role="alert" className="warn">{warn}</p>}
        <button className="primary" disabled={busy}>{warn ? 'Repoint anyway' : existing ? 'Save training' : 'Register training'}</button>
        {stale && <Conflict onReload={() => { setStale(false); reload() }} message="Someone changed this, reload to see the latest." />}
        <p role="status" className={bad ? 'error' : 'pass'}>{status}</p>
      </form>
    </section>
  )
}
