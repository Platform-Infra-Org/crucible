import { Link, useParams } from 'react-router'
import { useFetch } from '../useFetch'
import type { ItemView, Outline } from '../types'
import { ErrorBox } from '../components/ErrorBox'
import { Loader } from '../components/Loader'
import { MoltenBar } from '../components/MoltenBar'

const statusLabel = { new: 'Cold', in_progress: 'Heating', complete: 'Forged' } as const

function itemPath(base: string, module: string, it: ItemView) {
  return it.kind === 'reading' ? `${base}/${module}/read/${it.id}` : `${base}/${module}/${it.kind}`
}

export function TrainingPage() {
  const { team, training } = useParams()
  const { data, error } = useFetch<Outline>(`/api/programs/${team}/${training}`)
  if (error) return <ErrorBox error={error} />
  if (!data) return <Loader label="Unrolling the blueprint…" />
  const base = `/p/${team}/${training}/m`
  return (
    <section className="page">
      <Link to="/">← Hearth</Link>
      <h1>{data.title}</h1>
      <p className="lede">{data.description}</p>
      <MoltenBar percent={data.percent} />
      <ol className="modules">
        {data.modules.map((m) => (
          <li key={m.id} className={`module ${m.locked ? 'locked' : ''} ${m.complete ? 'complete' : ''}`} data-testid={`module-${m.id}`} data-locked={m.locked}>
            <h2>
              <span aria-hidden="true">{m.locked ? '🔒' : m.complete ? '✦' : '◆'}</span> {m.title}
            </h2>
            <ul>
              {m.items.map((it) => (
                <li key={it.kind + it.id} className="item">
                  {m.locked ? (
                    <span className="muted">{it.kind === 'reading' ? it.title : it.kind === 'quiz' ? 'Quiz' : 'Lab'}</span>
                  ) : (
                    <Link to={itemPath(base, m.id, it)}>{it.kind === 'reading' ? it.title : it.kind === 'quiz' ? 'Quiz' : 'Lab'}</Link>
                  )}
                  <span className={`badge ${it.status}`}>{statusLabel[it.status]}</span>
                </li>
              ))}
            </ul>
          </li>
        ))}
      </ol>
    </section>
  )
}
