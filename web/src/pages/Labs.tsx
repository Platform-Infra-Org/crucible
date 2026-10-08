import { Link } from 'react-router'
import { useFetch } from '../useFetch'
import type { MyLab } from '../types'
import { ErrorBox } from '../components/ErrorBox'
import { Loader } from '../components/Loader'

const active = ['pending_approval', 'provisioning', 'ready', 'destroying']
const when = (s: string) => new Date(s).toLocaleString()

export function LabsPage() {
  const { data, error } = useFetch<MyLab[]>('/api/labs', 15_000)
  if (error) return <ErrorBox error={error} />
  if (!data) return <Loader label="Finding your labs…" />
  return (
    <section className="page">
      <h1>Labs</h1>
      {data.length === 0 ? <p className="muted empty">No labs yet.</p> : (
        <div className="table-wrap"><table className="grid">
          <thead><tr><th>Lab</th><th>Training</th><th>State</th><th>Started</th><th>Ends</th><th /></tr></thead>
          <tbody>
            {data.map((l) => (
              <tr key={l.id}>
                <td>{l.title}</td>
                <td>{l.training}</td>
                <td><span className="badge">{l.state.replaceAll('_', ' ')}</span></td>
                <td>{when(l.created_at)}</td>
                <td>{l.ends_at && active.includes(l.state) ? when(l.ends_at) : ''}</td>
                <td><Link to={l.link}>Open</Link></td>
              </tr>
            ))}
          </tbody>
        </table></div>
      )}
    </section>
  )
}
