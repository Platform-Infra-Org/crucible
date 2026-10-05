export type TimerState = 'normal' | 'cooling' | 'critical' | 'expired'

const MIN = 60_000

// offset = server clock − client clock; add it to Date.now() to get server time.
export function clockOffset(serverNowIso: string, clientNow: number): number {
  return Date.parse(serverNowIso) - clientNow
}

export function remainingMs(endsAtIso: string, offset: number, clientNow: number): number {
  return Math.max(0, Date.parse(endsAtIso) - (clientNow + offset))
}

export function timerState(ms: number): TimerState {
  if (ms <= 0) return 'expired'
  if (ms <= 5 * MIN) return 'critical'
  if (ms <= 15 * MIN) return 'cooling'
  return 'normal'
}

export function formatRemaining(ms: number): string {
  const s = Math.ceil(ms / 1000)
  const h = Math.floor(s / 3600)
  const m = String(Math.floor((s % 3600) / 60)).padStart(2, '0')
  const sec = String(s % 60).padStart(2, '0')
  return h > 0 ? `${h}:${m}:${sec}` : `${m}:${sec}`
}

export function crossedWarnings(prevMs: number, ms: number): number[] {
  return [15, 5].filter((min) => prevMs > min * MIN && ms <= min * MIN)
}

export function idleWarningVisible(deadlineIso: string, warnS: number, offset: number, now: number): boolean {
  const left = Date.parse(deadlineIso) - (now + offset)
  return left > 0 && left <= warnS * 1000
}
