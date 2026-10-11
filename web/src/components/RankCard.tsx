import { useEffect } from 'react'
import { motion } from 'motion/react'
import { api } from '../api'
import { useCalm } from '../me'
import type { Forge } from '../types'
import { article } from '../lib/forge'
import { MoltenBar } from './MoltenBar'
import { SmithMark } from './SmithMark'

export function ExperienceMeter({ smith }: { smith: Forge['smith'] }) {
  const pct = smith.next ? Math.min(100, Math.floor((smith.xp / smith.next.at) * 100)) : 100
  const caption = smith.next ? `${smith.xp.toLocaleString()} / ${smith.next.at.toLocaleString()} to ${smith.next.title}` : `${smith.xp.toLocaleString()} experience`
  return <MoltenBar percent={pct} label="Experience" caption={caption} />
}

export function CraftCard({ forge }: { forge: Forge }) {
  return (
    <section className="rank-card craft" aria-labelledby="craft-heading">
      <h2 id="craft-heading">Your craft</h2>
      <p className="rank-name"><SmithMark smith={forge.smith} /></p>
      <ExperienceMeter smith={forge.smith} />
      <p className="muted">{forge.smith.next ? `Next: ${forge.smith.next.title} at ${forge.smith.next.at.toLocaleString()} experience` : 'The highest rank. The forge salutes you.'}</p>
    </section>
  )
}

export function SwordList({ forge }: { forge: Forge }) {
  return (
    <section className="rank-card swords" aria-labelledby="swords-heading">
      <h2 id="swords-heading">Your swords</h2>
      {forge.swords.length === 0 ? <p className="muted empty">No trainings yet: each one you take forges a sword.</p> : (
        <ul className="sword-list">
          {forge.swords.map((s) => (
            <li key={s.training}>
              <strong>{s.title}</strong> <span className="badge">{s.rank}</span> <span className="muted">{s.percent}%{s.next ? ` · ${s.next.name} at ${s.next.at}%` : ' · finished'}</span>
            </li>
          ))}
        </ul>
      )}
      {forge.badges.length > 0 && (
        <ul className="badges" data-testid="badges" aria-label="Badges">
          {forge.badges.map((b) => <li key={b.training} className="badge-token"><span aria-hidden="true">🏅</span> {b.title} <span className="muted">· earned {new Date(b.earned_at).toLocaleDateString()}</span></li>)}
        </ul>
      )}
    </section>
  )
}

// RankUp celebrates a new rank once: hammer strike + glow, or a static line under calm motion. It never blocks the page or takes focus. Mount it inside a persistent role="status" region (see RankUpSlot) so it is announced.
// title is the new rank's name; training (a sword) is its id for the server and its title for the words.
export function RankUp({ kind, training, title, level, onDone }: { kind: 'smith' | 'sword'; training?: { id: string; title: string }; title: string; level: number; onDone: () => void }) {
  const calm = useCalm()
  useEffect(() => {
    api('/api/me/forge/seen', { method: 'POST', json: { kind, training: training?.id ?? '', level } }).catch(() => {})
  }, [kind, training?.id, level])
  return (
    <section className="rank-up" aria-label="Rank up">
      {!calm && (
        <motion.span className="hammer" aria-hidden="true" initial={{ rotate: -50, y: -10 }} animate={{ rotate: [-50, 8, 0], y: [-10, 2, 0] }} transition={{ duration: 0.6, ease: 'easeIn' }}>
          🔨
        </motion.span>
      )}
      {!calm && <motion.span className="strike-glow" aria-hidden="true" initial={{ scale: 0.4, opacity: 0.9 }} animate={{ scale: 2.2, opacity: 0 }} transition={{ duration: 0.9, delay: 0.45 }} />}
      <strong>{kind === 'smith' ? `You are now ${article(title)} ${title}.` : `Your ${training?.title} sword is now ${article(title)} ${title}.`}</strong>{' '}
      <span className="muted">{kind === 'smith' ? 'Your craft grows.' : 'Steel is forged in fire.'}</span>
      <button className="ghost" onClick={onDone}>Nice</button>
    </section>
  )
}

// RankUpSlot is the always-mounted live region; the banner is inserted into it after the forge fetch, which screen readers announce.
export function RankUpSlot({ children }: { children?: React.ReactNode }) {
  return <div role="status" aria-live="polite">{children}</div>
}
