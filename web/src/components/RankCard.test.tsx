import { describe, expect, test } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { CraftCard, SwordList, RankUp, RankUpSlot } from './RankCard'
import { CalmContext } from '../me'
import type { Forge } from '../types'

const forge: Forge = {
  smith: { xp: 720, level: 2, title: 'Journeyman', next: { title: 'Smith', at: 1200 } },
  swords: [
    { training: 'forge-101', title: 'Forge 101', level: 2, rank: 'Tempered', percent: 52, next: { name: 'Blade', at: 75 } },
    { training: 'forge-102', title: 'Forge 102', level: 5, rank: 'Masterwork', percent: 100, next: null },
  ],
  badges: [{ training: 'forge-102', title: 'Forge 102: Sparks', earned_at: '2026-10-06T10:00:00Z' }],
  sword_ladder: [], smith_ladder: [],
  rank_up: { smith: null, swords: [] },
}
const noPosition = /leaderboard|rank #|position/i // ranks are personal (spec §7)

describe('CraftCard', () => {
  test('shows the smith title, the way to the next rank and a meter', () => {
    const html = renderToStaticMarkup(<CraftCard forge={forge} />)
    expect(html).toContain('Your craft')
    expect(html).toContain('Journeyman')
    expect(html).toContain('720 / 1,200 to Smith')
    expect(html).toContain('role="progressbar"')
    expect(html).not.toMatch(noPosition)
  })
  test('a Grandmaster has no next rank', () => {
    const html = renderToStaticMarkup(<CraftCard forge={{ ...forge, smith: { xp: 5200, level: 5, title: 'Grandmaster', next: null } }} />)
    expect(html).toContain('The highest rank')
    expect(html).not.toContain(' to ')
  })
})

describe('SwordList', () => {
  test('one line per training with its rank, percent and next rank; badges below', () => {
    const html = renderToStaticMarkup(<SwordList forge={forge} />)
    expect(html).toContain('Your swords')
    expect(html).toContain('Forge 101')
    expect(html).toContain('Tempered')
    expect(html).toContain('52%')
    expect(html).toContain('Blade at 75%')
    expect(html).toContain('finished')
    expect(html).toContain('Forge 102: Sparks')
    expect(html).toContain('earned ')
    expect(html).not.toMatch(noPosition)
  })
  test('no trainings is said plainly', () => {
    expect(renderToStaticMarkup(<SwordList forge={{ ...forge, swords: [], badges: [] }} />)).toContain('No trainings yet')
  })
})

describe('RankUp', () => {
  test('a smith rise is a status banner, not a dialog', () => {
    const html = renderToStaticMarkup(<RankUp kind="smith" title="Journeyman" level={2} onDone={() => {}} />)
    expect(renderToStaticMarkup(<RankUpSlot />)).toBe('<div role="status" aria-live="polite"></div>')
    expect(html).not.toContain('role="status"')
    expect(html).not.toContain('role="dialog"')
    expect(html).toContain('You are now a Journeyman')
    expect(html).toContain('class="hammer"')
  })
  test('a sword rise names the training and the new rank', () => {
    const html = renderToStaticMarkup(<RankUp kind="sword" training={{ id: 'forge-101', title: 'Forge 101' }} title="Blade" level={3} onDone={() => {}} />)
    expect(html).toContain('Your Forge 101 sword is now a Blade')
  })
  test('calm motion drops the hammer and glow but keeps the message', () => {
    const html = renderToStaticMarkup(<CalmContext value={true}><RankUp kind="smith" title="Apprentice" level={1} onDone={() => {}} /></CalmContext>)
    expect(html).not.toContain('hammer')
    expect(html).not.toContain('strike-glow')
    expect(html).toContain('You are now an Apprentice')
  })
})
