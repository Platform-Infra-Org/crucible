import { Link } from 'react-router'
import { useFetch } from '../useFetch'
import type { CatalogEntry } from '../types'
import { ErrorBox } from '../components/ErrorBox'
import { Loader } from '../components/Loader'

export function TrainingsPage() {
  const { data, error } = useFetch<CatalogEntry[]>('/api/catalog')
  if (error) return <ErrorBox error={error} />
  if (!data) return <Loader label="Opening the catalog…" />
  return (
    <section className="page">
      <h1>Trainings</h1>
      <div className="cards">
        {data.map((c) => (
          <article key={c.id} className="card" data-testid={`catalog-${c.id}`}>
            <h2>{c.title}</h2>
            {c.description && <p>{c.description}</p>}
            <p className="muted">
              {c.estimated_hours > 0 && <>≈ {c.estimated_hours} h · </>}
              {c.modules} {c.modules === 1 ? 'module' : 'modules'}
            </p>
            {c.enrolled.length > 0
              ? c.enrolled.map((t) => <p key={t.id}><Link to={`/p/${t.id}/${c.id}`}>Open ({t.name})</Link></p>)
              : <p className="muted">Ask your team leader to enrol you.</p>}
          </article>
        ))}
      </div>
    </section>
  )
}
