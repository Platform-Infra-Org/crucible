import { motion } from 'motion/react'

export function MoltenBar({ percent, label = 'Progress', caption }: { percent: number; label?: string; caption?: string }) {
  return (
    <div>
      <div className="molten-bar" role="progressbar" aria-valuenow={percent} aria-valuemin={0} aria-valuemax={100} aria-label={label}>
        <motion.div className="fill" initial={{ width: 0 }} animate={{ width: `${percent}%` }} transition={{ duration: 1.2, ease: 'easeOut' }} />
      </div>
      <small className="muted">{caption ?? `${percent}% forged`}</small>
    </div>
  )
}
