import { useEffect, useState } from 'react'
import { AnimatePresence, motion } from 'motion/react'

export function Toaster() {
  const [items, setItems] = useState<{ id: number; msg: string }[]>([])
  useEffect(() => {
    const on = (e: Event) => {
      const id = Date.now() + Math.random()
      setItems((xs) => [...xs, { id, msg: (e as CustomEvent<string>).detail }])
      setTimeout(() => setItems((xs) => xs.filter((x) => x.id !== id)), 6000)
    }
    window.addEventListener('crucible-toast', on)
    return () => window.removeEventListener('crucible-toast', on)
  }, [])
  return (
    <div className="toasts" role="status" aria-live="polite">
      <AnimatePresence>
        {items.map((t) => (
          <motion.div key={t.id} className="toast" initial={{ opacity: 0, y: 12 }} animate={{ opacity: 1, y: 0 }} exit={{ opacity: 0 }}>
            {t.msg}
          </motion.div>
        ))}
      </AnimatePresence>
    </div>
  )
}
