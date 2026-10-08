import { Link } from 'react-router'
import { useMe } from '../me'
import { useFetch } from '../useFetch'
import type { CatalogEntry } from '../types'
import { ErrorBox } from '../components/ErrorBox'
import { Loader } from '../components/Loader'

export function TrainingsPage() {
  const { data, error } = useFetch<CatalogEntry[]>('/api/catalog')
  const { me } = useMe()
  // Manage trainings lists the trainings of the caller's teams; an author on no team edits from the Edits list.
  const manage = (me.can_manage_trainings || me.can_edit_content) && (me.is_admin || me.teams.length > 0)
  if (error) return <ErrorBox error={error} />
  if (!data) return <Loader label="Opening the catalog…" />
  return (
    <section className="page">
      <div className="row">
        <h1>Trainings</h1>
        <span className="spacer" />
        {(me.can_manage_trainings || me.can_edit_content) && (
          <Link className="button-link" to={manage ? '/trainings/manage' : '/edits'}>Manage trainings</Link>
        )}
      </div>
      {data.length === 0 && <p className="muted">No trainings are available yet.</p>}
      <div className="cards">
        {data.map((c) => (
          <article key={c.id} className={manage ? 'card card-link' : 'card'} data-testid={`catalog-${c.id}`}>
            {/* the title's link covers the card (CSS); the Open links stay on top of it */}
            <h2>{manage ? <Link to={`/trainings/manage/${c.id}`}>{c.title}</Link> : c.title}</h2>
            {c.description && <p>{c.description}</p>}
            {!c.available && <p className="warn">Content unavailable right now</p>}
            {c.available && <p className="muted">
              {c.estimated_hours > 0 && <>≈ {c.estimated_hours} h · </>}
              {c.modules} {c.modules === 1 ? 'module' : 'modules'}
            </p>}
            {c.available && (c.enrolled.length > 0
              ? c.enrolled.map((t) => <p key={t.id}><Link to={`/p/${t.id}/${c.id}`}>Open ({t.name})</Link></p>)
              : <p className="muted">Ask your team leader to enrol you.</p>)}
          </article>
        ))}
      </div>
    </section>
  )
}
