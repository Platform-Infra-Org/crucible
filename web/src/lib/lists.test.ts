import { describe, expect, it } from 'vitest'
import { formatMentors, parseEmails, parseMentors } from './lists'

describe('list fields', () => {
  it('reads one email per line or comma separated, lowercased, without duplicates', () => {
    expect(parseEmails(' A@x.io\nb@x.io, a@x.io\n\n')).toEqual(['a@x.io', 'b@x.io'])
  })
  it('reads and writes "trainee = mentor" lines', () => {
    const m = parseMentors('T@x = S@x\n\n u@x=s@x ')
    expect(m).toEqual({ 't@x': 's@x', 'u@x': 's@x' })
    expect(formatMentors(m)).toBe('t@x = s@x\nu@x = s@x')
  })
  it('rejects a line without "="', () => {
    expect(() => parseMentors('t@x s@x')).toThrow(/trainee = mentor/)
  })
})
