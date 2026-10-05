import { motion } from 'motion/react'
import { useCalm } from '../me'

export function SparkBurst({ trigger }: { trigger: number }) {
  const calm = useCalm()
  if (!trigger || calm) return null
  return (
    <div className="sparks" aria-hidden="true" key={trigger}>
      {Array.from({ length: 14 }, (_, i) => {
        const a = (i / 14) * Math.PI * 2
        return (
          <motion.span key={i} initial={{ x: 0, y: 0, opacity: 1, scale: 1 }} animate={{ x: Math.cos(a) * 70, y: Math.sin(a) * 70, opacity: 0, scale: 0.4 }} transition={{ duration: 0.8, ease: 'easeOut' }} />
        )
      })}
    </div>
  )
}
