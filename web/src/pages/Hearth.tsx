import { useMemo, useState } from 'react'
import { Link } from 'react-router'
import { useMe } from '../App'
import { useFetch } from '../useFetch'
import type { Forge, ProgramCard } from '../types'
import { randomQuote } from '../lib/quotes'
import { Embers } from '../components/Embers'
import { Loader } from '../components/Loader'
import { MoltenBar } from '../components/MoltenBar'
import { RankCard, RankUp } from '../components/RankCard'

export function Hearth() {
  const { me } = useMe()
  const { data, error } = useFetch<ProgramCard[]>('/api/programs')
  const forge = useFetch<Forge>('/api/me/forge') // an error shows nothing; the cards still render
  const [dismissed, setDismissed] = useState(false)
  const quote = useMemo(randomQuote, [])
  const first = (me.user.name || me.user.email).split(/[ @]/)[0]
  return (
    <section className="page">
      <Embers count={12} />
      <h1>Hearth</h1>
      {forge.data?.rank_up && !dismissed && <RankUp rank={forge.data.rank} onDone={() => setDismissed(true)} />}
      <p className="lede">
        Welcome back, {first}. <em>“{quote}”</em>
      </p>
      {forge.data && <RankCard forge={forge.data} />}
      {error && <p className="error">{error.message}</p>}
      {!data && !error && <Loader label="Gathering your trainings…" />}
      {data && data.length === 0 && <p className="muted">You're not enrolled in any training yet. Your team leader can enroll you.</p>}
      {data && data.length > 0 && (
        <div className="cards">
          {data.map((c) => (
            <Link key={c.team + c.training} to={`/p/${c.team}/${c.training}`} className="card" aria-label={c.title}>
              <h2>{c.title}</h2>
              <p className="muted">{c.team_name}</p>
              <p>{c.description}</p>
              <MoltenBar percent={c.percent} />
              {!c.available && <p className="warn">Content unavailable right now</p>}
            </Link>
          ))}
        </div>
      )}
    </section>
  )
}
