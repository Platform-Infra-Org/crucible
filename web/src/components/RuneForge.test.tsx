import { expect, test } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { RuneForge } from './RuneForge'

test('the rune forge starts at ore: the stone ring, six runestones, the first rune awake, the ore in the circle', () => {
  const h = renderToStaticMarkup(<RuneForge calm={false} />)
  expect(h).toContain('class="rune-forge s0"')
  expect(h.match(/class="rf-rune/g)).toHaveLength(6) // one rune per rank
  expect(h.match(/class="rf-rune lit/g)).toHaveLength(1)
  expect(h).toContain('class="rf-piece rf-p0"') // the ore, inside the circle
  expect(h).not.toContain('rf-enchant') // the magic waits for the completed circle
  expect(h).not.toContain('crucible')
  expect(h).toContain('<li class="now">Ore</li>')
  expect(h).toContain('class="sr-only">An animation of a rune forge')
})

test('under calm motion it rests on the enchanted masterwork, every rune lit, no motes', () => {
  const h = renderToStaticMarkup(<RuneForge calm={true} />)
  expect(h).toContain('class="rune-forge s5"')
  expect(h.match(/class="rf-rune lit/g)).toHaveLength(6)
  expect(h).toContain('class="rf-enchant"')
  expect(h).toContain('class="rf-bolt') // lightning, still
  expect(h).not.toContain('rf-mote') // motes only move, so none
  expect(h).toContain('<li class="now">Masterwork</li>')
})
