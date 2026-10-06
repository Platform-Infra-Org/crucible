import { useEffect } from 'react'
import { motion } from 'motion/react'
import { api } from '../api'
import { useCalm } from '../me'
import type { Forge } from '../types'
import { MoltenBar } from './MoltenBar'

export function RankCard({ forge }: { forge: Forge }) {
  const next = forge.ladder[forge.level + 1]
  return (
    <section className="rank-card" aria-labelledby="rank-heading">
      <h2 id="rank-heading" className="sr-only">Your forge rank</h2>
      <p className="rank-name"><span aria-hidden="true">⚒</span> <strong data-testid="rank">{forge.rank}</strong></p>
      <MoltenBar percent={Math.floor(forge.percent)} label="Training forged" caption={`${forge.percent}% of your training forged`} />
      <p className="muted">{next ? `Next: ${next.name} at ${next.at}%` : 'The highest rank. The forge salutes you.'}</p>
      {forge.badges.length > 0 && (
        <ul className="badges" data-testid="badges" aria-label="Badges">
          {forge.badges.map((b) => <li key={b.training} className="badge-token"><span aria-hidden="true">🏅</span> {b.title} <span className="muted">· earned {new Date(b.earned_at).toLocaleDateString()}</span></li>)}
        </ul>
      )}
    </section>
  )
}

// RankUp celebrates a new rank once: hammer strike + glow, or a static line under calm motion. It never blocks the page or takes focus. Mount it inside a persistent role="status" region (see RankUpSlot) so it is announced.
export function RankUp({ rank, level, onDone }: { rank: string; level: number; onDone: () => void }) {
  const calm = useCalm()
  useEffect(() => {
    api('/api/me/forge/seen', { method: 'POST', json: { level } }).catch(() => {})
  }, [level])
  return (
    <section className="rank-up" aria-label="Rank up">
      {!calm && (
        <motion.span className="hammer" aria-hidden="true" initial={{ rotate: -50, y: -10 }} animate={{ rotate: [-50, 8, 0], y: [-10, 2, 0] }} transition={{ duration: 0.6, ease: 'easeIn' }}>
          🔨
        </motion.span>
      )}
      {!calm && <motion.span className="strike-glow" aria-hidden="true" initial={{ scale: 0.4, opacity: 0.9 }} animate={{ scale: 2.2, opacity: 0 }} transition={{ duration: 0.9, delay: 0.45 }} />}
      <strong>You reached {rank}.</strong> <span className="muted">Steel is forged in fire.</span>
      <button className="ghost" onClick={onDone}>Nice</button>
    </section>
  )
}

// RankUpSlot is the always-mounted live region; the banner is inserted into it after the forge fetch, which screen readers announce.
export function RankUpSlot({ children }: { children?: React.ReactNode }) {
  return <div role="status" aria-live="polite">{children}</div>
}
