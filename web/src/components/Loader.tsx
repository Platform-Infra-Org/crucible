import { useEffect, useState } from 'react'
import { AnimatePresence, motion } from 'motion/react'
import { randomQuote } from '../lib/quotes'
import { Embers } from './Embers'

const BOWL = 'M20 30 h80 l-10 70 a10 10 0 0 1 -10 8 h-40 a10 10 0 0 1 -10 -8z'

export function Loader({ label, lines }: { label: string; lines?: string[] }) {
  const [quote, setQuote] = useState(randomQuote)
  useEffect(() => {
    const id = setInterval(() => setQuote(randomQuote()), 4500)
    return () => clearInterval(id)
  }, [])
  return (
    <div className="loader" role="status" aria-live="polite">
      <Embers />
      <svg className="crucible" viewBox="0 0 120 120" aria-hidden="true">
        <defs>
          <clipPath id="bowl">
            <path d={BOWL} />
          </clipPath>
        </defs>
        <g clipPath="url(#bowl)">
          <rect className="molten" x="0" y="30" width="120" height="90" />
        </g>
        <path className="crucible-shell" d={BOWL} />
      </svg>
      <p className="loader-label">{label}</p>
      <AnimatePresence mode="wait">
        <motion.blockquote key={quote} initial={{ opacity: 0, y: 6 }} animate={{ opacity: 1, y: 0 }} exit={{ opacity: 0, y: -6 }} transition={{ duration: 0.5 }}>
          “{quote}”
        </motion.blockquote>
      </AnimatePresence>
      {lines && lines.length > 0 && <pre className="loader-log">{lines.join('\n')}</pre>}
    </div>
  )
}
