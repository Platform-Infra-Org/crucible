import { useMemo, useRef, useState } from 'react'
import { Link } from 'react-router'
import { useMe } from '../App'
import { useFetch } from '../useFetch'
import type { Forge, ProgramCard } from '../types'
import { randomQuote } from '../lib/quotes'
import { Embers } from '../components/Embers'
import { Loader } from '../components/Loader'
import { MoltenBar } from '../components/MoltenBar'
import { CraftCard, SwordList, RankUp, RankUpSlot } from '../components/RankCard'

export function Hearth() {
  const { me } = useMe()
  const { data, error } = useFetch<ProgramCard[]>('/api/programs')
  const forge = useFetch<Forge>('/api/me/forge') // an error shows nothing; the cards still render
  const [done, setDone] = useState<string[]>([]) // banners dismissed this visit
  const h1 = useRef<HTMLHeadingElement>(null)
  const quote = useMemo(randomQuote, [])
  const first = (me.user.name || me.user.email).split(/[ @]/)[0]
  // One banner at a time: the smith rise first, then each sword's, in order; dismissing shows the next.
  const up = forge.data?.rank_up
  const finish = (k: string) => () => { setDone((d) => [...d, k]); h1.current?.focus() }
  const sword = up?.swords.find((w) => !done.includes(`sword:${w.training}`))
  const banner = up?.smith && !done.includes('smith')
    ? <RankUp kind="smith" title={up.smith.title} level={up.smith.level} onDone={finish('smith')} />
    : sword && <RankUp key={sword.training} kind="sword" training={{ id: sword.training, title: sword.title }} title={sword.rank} level={sword.level} onDone={finish(`sword:${sword.training}`)} />
  return (
    <section className="page">
      <Embers count={12} />
      <h1 ref={h1} tabIndex={-1}>Hearth</h1>
      <RankUpSlot>
        {banner}
      </RankUpSlot>
      <p className="lede">
        Welcome back, {first}. <em>“{quote}”</em>
      </p>
      <div className="forge-slot">{forge.data && <><CraftCard forge={forge.data} /><SwordList forge={forge.data} /></>}</div>
      {error && <p className="error">{error.message}</p>}
      {!data && !error && <Loader label="Gathering your trainings…" />}
      {data && data.length === 0 && <p className="muted">You're not taking any training yet. The people who run a training make you a participant.</p>}
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
