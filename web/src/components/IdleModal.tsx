import { useEffect } from 'react'
import { AnimatePresence, motion } from 'motion/react'
import type { LabView } from '../types'
import { formatRemaining, idleWarningVisible } from '../lib/timer'
import { alertUser } from '../lib/alerts'
import { useNow } from './Timer'

export function IdleModal({ lab, offset, onHere }: { lab: LabView; offset: number; onHere: () => void }) {
  const now = useNow()
  const visible = !!lab.idle_deadline && idleWarningVisible(lab.idle_deadline, lab.idle_warning_s, offset, now)
  const left = lab.idle_deadline ? Date.parse(lab.idle_deadline) - (now + offset) : 0
  useEffect(() => {
    if (visible) alertUser('Are you still there? Your lab is cooling down.', 'Still there?')
  }, [visible])
  return (
    <AnimatePresence>
      {visible && (
        <motion.div className="modal-backdrop" initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }}>
          <div className="modal" role="alertdialog" aria-modal="true" aria-labelledby="idle-title">
            <h2 id="idle-title">Are you still there?</h2>
            <p>
              The forge is cooling… your lab closes in <strong>{formatRemaining(left)}</strong>.
            </p>
            <button className="primary" autoFocus onClick={onHere}>I'm here</button>
          </div>
        </motion.div>
      )}
    </AnimatePresence>
  )
}
