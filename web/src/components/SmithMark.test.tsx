import { describe, expect, test } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { SmithMark } from './SmithMark'

describe('the smith mark', () => {
  test('carries the title in words and its level as a class', () => {
    const h = renderToStaticMarkup(<SmithMark smith={{ level: 2, title: 'Journeyman' }} />)
    expect(h).toContain('Journeyman')
    expect(h).toContain('smith-mark l2')
  })
  test('a person with no smith rank yet is a Bellows Hand, and nothing is said about position', () => {
    const h = renderToStaticMarkup(<SmithMark smith={{ level: 0, title: 'Bellows Hand' }} />)
    expect(h).toContain('Bellows Hand')
    expect(h).not.toMatch(/rank #|top |leaderboard/i)
  })
  test('nothing when the payload has no smith rank', () => {
    expect(renderToStaticMarkup(<SmithMark />)).toBe('')
  })
})
