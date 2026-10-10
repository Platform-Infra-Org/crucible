import { expect, test } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { Pour } from './Pour'

test('the rune forge starts at ore: crucible, upright sword mould, arcane circle, the first rune awake', () => {
  const h = renderToStaticMarkup(<Pour calm={false} />)
  expect(h).toContain('class="pour s0"')
  expect(h).toContain('class="pour-crucible"')
  expect(h).toContain('class="pour-mould"')
  expect(h).toContain('class="pour-ring"')
  expect(h.match(/class="pour-rune/g)).toHaveLength(6) // one rune per rank
  expect(h.match(/class="pour-rune lit/g)).toHaveLength(1)
  expect(h).toContain('<li class="now">Ore</li>')
  expect(h).toContain('class="sr-only">An animation of a rune forge')
})

test('under calm motion it rests on the masterwork, every rune lit', () => {
  const h = renderToStaticMarkup(<Pour calm={true} />)
  expect(h).toContain('class="pour s5"')
  expect(h.match(/class="pour-rune lit/g)).toHaveLength(6)
  expect(h).toContain('<li class="now">Masterwork</li>')
})
