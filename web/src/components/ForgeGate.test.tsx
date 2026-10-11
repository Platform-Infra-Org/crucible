import { expect, test } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { ForgeGate } from './ForgeGate'

test('the gate is the title at the centre and the way in, over drifting embers', () => {
  const h = renderToStaticMarkup(<ForgeGate calm={false} />)
  expect(h).toContain('class="gate-title"')
  expect(h).toContain('Crucible')
  expect(h).toContain('Enter the forge')
  expect(h).toContain('You sign in with your company account.')
  expect(h).toContain('class="embers"')
  expect(h).not.toContain('<svg') // no animation of the ranks
  expect(h).not.toContain('aria-pressed') // nothing to choose
})

test('under calm motion the embers are gone', () => {
  const h = renderToStaticMarkup(<ForgeGate calm={true} />)
  expect(h).toContain('Enter the forge')
  expect(h).not.toContain('class="embers"')
})
