import { expect, test } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { RuneSword } from './RuneSword'

test('the rune sword starts at ore: a horizontal sword with six engraved runes, the first lit in its own colour', () => {
  const h = renderToStaticMarkup(<RuneSword calm={false} />)
  expect(h).toContain('class="rune-sword s0"')
  expect(h.match(/class="rs-rune/g)).toHaveLength(6) // one rune per rank, engraved on the blade
  expect(h.match(/class="rs-rune lit/g)).toHaveLength(1)
  for (let n = 1; n <= 6; n++) expect(h).toContain(`--c:var(--rune-${n})`) // each rune its own colour
  expect(h).not.toContain('rs-enchant') // the magic waits for all six
  expect(h).toContain('<li class="now">Ore</li>')
  expect(h).toContain('class="sr-only">An animation of a runic sword')
})

test('under calm motion it rests on the enchanted masterwork: every rune lit, the aura and lightning still, no motes', () => {
  const h = renderToStaticMarkup(<RuneSword calm={true} />)
  expect(h).toContain('class="rune-sword s5"')
  expect(h.match(/class="rs-rune lit/g)).toHaveLength(6)
  expect(h).toContain('class="rs-enchant"')
  expect(h).toContain('class="rs-aura"')
  expect(h).toContain('class="rs-bolt')
  expect(h).not.toContain('rs-mote')
  expect(h).toContain('<li class="now">Masterwork</li>')
})
