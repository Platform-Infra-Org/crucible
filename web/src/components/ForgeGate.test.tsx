import { expect, test } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { ForgeGate } from './ForgeGate'

test('the gate offers the way in and shows the six ranks being forged', () => {
  const h = renderToStaticMarkup(<ForgeGate calm={false} />)
  expect(h).toContain('Enter the forge')
  expect(h).toContain('You sign in with your company account.')
  for (const r of ['Ore', 'Ingot', 'Tempered', 'Blade', 'Sword', 'Masterwork']) expect(h).toContain(`>${r}</li>`)
  expect(h).toContain('<li class="now">Ore</li>') // the forging starts from ore
  expect(h).toContain('class="sr-only">An animation of a hammer forging ore')
  expect(h).toContain('class="forge-hammer"') // at rest until the first strike
  expect(h).toContain('class="embers"')
})

test('under calm motion the masterwork rests on the anvil, nothing moves', () => {
  const h = renderToStaticMarkup(<ForgeGate calm={true} />)
  expect(h).toContain('<li class="now">Masterwork</li>')
  expect(h).toContain('The forge’s finest')
  expect(h).not.toContain('swinging')
  expect(h).not.toContain('forge-spark')
  expect(h).not.toContain('class="embers"')
})

test('two animations to choose from; the anvil when nothing is remembered', () => {
  const h = renderToStaticMarkup(<ForgeGate calm={false} />)
  expect(h).toContain('role="group" aria-label="Animation"')
  expect(h).toContain('aria-pressed="true">Anvil')
  expect(h).toContain('aria-pressed="false">Crucible')
})
