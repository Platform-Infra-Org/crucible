import { describe, expect, test } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { RankCard, RankUp, RankUpSlot } from './RankCard'
import { CalmContext } from '../me'
import type { Forge } from '../types'

const ladder = [
  { name: 'Ore', at: 0 }, { name: 'Ingot', at: 20 }, { name: 'Tempered', at: 45 },
  { name: 'Blade', at: 75 }, { name: 'Sword', at: 90 }, { name: 'Masterwork', at: 100 },
]
const forge: Forge = { percent: 50, level: 2, rank: 'Tempered', ladder, badges: [{ training: 'forge-102', title: 'Forge 102: Sparks', earned_at: '2026-10-06T10:00:00Z' }], rank_up: false }

describe('RankCard', () => {
  test('shows the rank, the way to the next one, and badges', () => {
    const html = renderToStaticMarkup(<RankCard forge={forge} />)
    expect(html).toContain('data-testid="rank"')
    expect(html).toContain('>Tempered<')
    expect(html).toContain('Blade at 75%')
    expect(html).toContain('Forge 102: Sparks')
    expect(html).toContain('earned ')
    expect(html).not.toContain('title=')
    expect(html).not.toMatch(/leaderboard|rank #|position/i) // ranks are personal (spec §7)
  })
  test('masterwork has no next rank', () => {
    const html = renderToStaticMarkup(<RankCard forge={{ ...forge, percent: 100, level: 5, rank: 'Masterwork' }} />)
    expect(html).toContain('The highest rank')
  })
  test('a rank-up is a status banner, not a dialog', () => {
    const html = renderToStaticMarkup(<RankUp rank="Blade" level={3} onDone={() => {}} />)
    expect(renderToStaticMarkup(<RankUpSlot />)).toBe('<div role="status" aria-live="polite"></div>')
    expect(html).not.toContain('role="status"')
    expect(html).not.toContain('role="dialog"')
    expect(html).toContain('You reached Blade')
    expect(html).toContain('class="hammer"')
  })
  test('calm motion drops the hammer and glow but keeps the message', () => {
    const html = renderToStaticMarkup(<CalmContext value={true}><RankUp rank="Blade" level={3} onDone={() => {}} /></CalmContext>)
    expect(html).not.toContain('hammer')
    expect(html).not.toContain('strike-glow')
    expect(html).toContain('You reached Blade')
  })
})
