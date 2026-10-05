import { describe, expect, it } from 'vitest'
import { clockOffset, crossedWarnings, formatRemaining, idleWarningVisible, remainingMs, timerState } from './timer'

const MIN = 60_000

describe('lab timer', () => {
  it('uses the server clock even when the laptop clock is 10 minutes fast', () => {
    const serverNow = '2026-10-05T09:00:00Z'
    const laptopNow = Date.parse(serverNow) + 10 * MIN // laptop is ahead
    const offset = clockOffset(serverNow, laptopNow)
    expect(remainingMs('2026-10-05T09:30:00Z', offset, laptopNow)).toBe(30 * MIN)
    expect(remainingMs('2026-10-05T09:30:00Z', offset, laptopNow + 31 * MIN)).toBe(0)
  })

  it('moves through normal → cooling → critical → expired', () => {
    expect(timerState(16 * MIN)).toBe('normal')
    expect(timerState(15 * MIN)).toBe('cooling')
    expect(timerState(5 * MIN)).toBe('critical')
    expect(timerState(0)).toBe('expired')
  })

  it('fires each warning exactly once when crossing 15 and 5 minutes', () => {
    expect(crossedWarnings(15 * MIN + 500, 15 * MIN - 500)).toEqual([15])
    expect(crossedWarnings(14 * MIN, 13 * MIN)).toEqual([])
    expect(crossedWarnings(5 * MIN + 1, 5 * MIN)).toEqual([5])
  })

  it('formats remaining time', () => {
    expect(formatRemaining(65 * MIN + 5000)).toBe('1:05:05')
    expect(formatRemaining(4 * MIN + 9000)).toBe('04:09')
    expect(formatRemaining(0)).toBe('00:00')
  })

  it('shows the idle prompt only inside the warning window', () => {
    const now = Date.parse('2026-10-05T09:00:00Z')
    expect(idleWarningVisible('2026-10-05T09:06:00Z', 300, 0, now)).toBe(false)
    expect(idleWarningVisible('2026-10-05T09:04:00Z', 300, 0, now)).toBe(true)
    expect(idleWarningVisible('2026-10-05T08:59:00Z', 300, 0, now)).toBe(false)
  })
})
