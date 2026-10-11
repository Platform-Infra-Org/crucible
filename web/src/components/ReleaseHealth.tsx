import type { Health, ReleaseStatus } from '../types'

const words: Record<ReleaseStatus, string> = { healthy: 'Ready', progressing: 'Starting', failing: 'Failing' }

// ReleaseHealth: each Helm release of a cluster lab and what is not ready yet, as of the last check.
export function ReleaseHealth({ health }: { health: Health }) {
  return (
    <section className="panel release-health" aria-labelledby="releases">
      <h5 id="releases">On your cluster</h5>
      <ul className="facts-list">
        {health.releases.map((r) => (
          <li key={r.name}>
            <strong>{r.name}</strong> <span className={`badge ${r.status === 'healthy' ? 'complete' : r.status === 'failing' ? 'warn' : 'in_progress'}`}>{words[r.status]}</span>
            {r.unready.length > 0 && <ul className="muted">{r.unready.map((u) => <li key={u}><code>{u}</code></li>)}</ul>}
          </li>
        ))}
      </ul>
    </section>
  )
}
