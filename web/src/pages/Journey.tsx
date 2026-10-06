import { useParams } from 'react-router'
import { useFetch } from '../useFetch'
import type { JourneyRow } from '../types'
import { ErrorBox } from '../components/ErrorBox'
import { Loader } from '../components/Loader'
import { HeatMap, Legend } from '../components/HeatMap'

export function JourneyPage() {
  const { team } = useParams()
  const { data, error } = useFetch<JourneyRow[]>(`/api/teams/${team}/journey`)
  if (error) return <ErrorBox error={error} />
  if (!data) return <Loader label="Reading the heat…" />
  return (
    <section className="page">
      <h1>Journey</h1>
      <Legend />
      <HeatMap rows={data} />
    </section>
  )
}
