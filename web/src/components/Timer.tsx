import { useEffect, useRef, useState } from 'react'
import type { LabView } from '../types'
import { crossedWarnings, formatRemaining, remainingMs, timerState } from '../lib/timer'
import { alertUser } from '../lib/alerts'

export function useNow(intervalMs = 1000) {
  const [now, setNow] = useState(Date.now())
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), intervalMs)
    return () => clearInterval(id)
  }, [intervalMs])
  return now
}

function limitLabel(lab: LabView) {
  if (!lab.ends_at) return ''
  const at = new Date(lab.ends_at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  if (lab.limit_reason === 'schedule') return `Ends at schedule close, ${at}`
  if (lab.limit_reason === 'budget') return `Ends at the budget cap, ${at}`
  return `Ends at ${at}`
}

export function Timer({ lab, offset, onExtend }: { lab: LabView; offset: number; onExtend: () => void }) {
  const now = useNow()
  const ms = lab.ends_at ? remainingMs(lab.ends_at, offset, now) : 0
  const known = !!lab.ends_at
  const prev = useRef(ms)
  useEffect(() => {
    for (const m of crossedWarnings(prev.current, ms)) alertUser(`${m} minutes left before your lab cools down`, `${m} min`)
    prev.current = ms
  }, [ms])
  return (
    <div className={`timer ${known ? timerState(ms) : ''}`} title={limitLabel(lab)}>
      <span aria-hidden="true">⏳</span> <strong data-testid="lab-timer">{known ? formatRemaining(ms) : '—'}</strong>
      <small className="muted">{limitLabel(lab)}</small>
      {lab.extension_pending && <span className="badge warn" role="status">Extension pending</span>}
      {lab.can_extend && <button className="ghost" onClick={onExtend}>Extend</button>}
    </div>
  )
}
