import { useFetch } from '../useFetch'
import type { Mentee } from '../types'
import { ErrorBox } from '../components/ErrorBox'
import { Loader } from '../components/Loader'
import { HeatMap, Legend } from '../components/HeatMap'

export function MentorPage() {
  const { data, error } = useFetch<Mentee[]>('/api/mentor')
  if (error) return <ErrorBox error={error} />
  if (!data) return <Loader label="Finding your mentees…" />
  return (
    <section className="page">
      <h1>Mentor</h1>
      {data.length === 0 && <p className="muted">You have no mentees yet.</p>}
      {data.length > 0 && <Legend />}
      {data.map((m) => (
        <section key={m.email} data-testid={`mentee-${m.email}`} aria-label={m.name || m.email}>
          <h2>{m.name || m.email} <small className="muted">{m.email}</small></h2>
          <p>Rank: {m.rank}</p>
          <HeatMap rows={m.programs} />
          <h3>Waiting for a scorer</h3>
          {m.pending.length === 0 ? <p className="muted">Nothing waiting.</p> : (
            <ul>{m.pending.map((p) => <li key={p.id}>{p.type}: {p.item} <span className="muted">({p.module}, {new Date(p.created_at).toLocaleDateString()})</span></li>)}</ul>
          )}
          <h3>Recent lab trouble</h3>
          {m.failures.length === 0 ? <p className="muted">No recent failures.</p> : (
            <ul>{m.failures.map((f, i) => <li key={i}>{f.detail} <span className="muted">({f.module}, {new Date(f.at).toLocaleDateString()})</span></li>)}</ul>
          )}
        </section>
      ))}
    </section>
  )
}
