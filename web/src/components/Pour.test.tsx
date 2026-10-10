import { expect, test } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { Pour } from './Pour'

test('the pour starts at ore, with the crucible and the sword mould', () => {
  const h = renderToStaticMarkup(<Pour calm={false} />)
  expect(h).toContain('class="pour s0"')
  expect(h).toContain('class="pour-crucible"')
  expect(h).toContain('class="pour-mould"')
  expect(h).toContain('<li class="now">Ore</li>')
  expect(h).toContain('class="sr-only">An animation of a crucible pouring molten metal into a sword mould')
  expect(h).not.toContain('forge-head') // no hammer: the pour is told in a few quiet lines
})

test('under calm motion it rests on the masterwork', () => {
  const h = renderToStaticMarkup(<Pour calm={true} />)
  expect(h).toContain('class="pour s5"')
  expect(h).toContain('<li class="now">Masterwork</li>')
})
