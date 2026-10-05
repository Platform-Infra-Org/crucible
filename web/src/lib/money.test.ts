import { describe, expect, it } from 'vitest'
import { hours, usd } from './money'

describe('money and time labels', () => {
  it('formats dollars with cents', () => {
    expect(usd(0.5)).toBe('$0.50')
    expect(usd(1234)).toBe('$1234.00')
  })
  it('formats lab lengths', () => {
    expect(hours(3600)).toBe('1h')
    expect(hours(5400)).toBe('1h 30m')
    expect(hours(1800)).toBe('30m')
  })
})
