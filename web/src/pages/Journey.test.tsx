import { expect, test, vi } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router'
import { JourneyPage } from './Journey'
import type { JourneyRow } from '../types'

const rows: JourneyRow[] = [
  { email: 't@x', name: 'Tara', avatar: 'flame', team: 'forge', training: 'f101', title: 'Forge 101', percent: 50,
    cells: [{ module: 'm1', title: 'Sparks', heat: 'forged' }, { module: 'm2', title: 'Embers', heat: 'cold' }],
    flags: [{ kind: 'inactive', detail: 'no activity for 5 business days' }] },
  { email: 'u@x', name: 'Uma', team: 'forge', training: 'f101', title: 'Forge 101', percent: 100, cells: [], flags: [] },
]
vi.mock('../useFetch', () => ({ useFetch: (path: string) => ({ data: path === '/api/teams' ? [{ id: 'forge', name: 'The Forge', role: 'leader' }] : rows, error: undefined }) }))
vi.mock('../me', () => ({ useMe: () => ({ me: { is_admin: false } }), useCalm: () => false }))
vi.mock('react-router', async (orig) => ({ ...(await orig<typeof import('react-router')>()), useParams: () => ({ team: 'forge' }) }))
const page = (q = '') => renderToStaticMarkup(<MemoryRouter initialEntries={[`/teams/forge/journey${q}`]}><JourneyPage /></MemoryRouter>)

test('by person: a panel each, with the molten bar, the cells, the flags and the numbers at a glance', () => {
  const h = page()
  expect(h).toContain('How The Forge is getting on')
  expect(h).toContain('<strong>2</strong> people')
  expect(h).toContain('<strong>75%</strong> forged on average')
  expect(h).toContain('<strong>1</strong> needs a look')
  expect(h).toMatch(/aria-label="Tara".*aria-label="Uma"/)
  expect(h).toContain('aria-valuenow="50"')
  expect(h).toContain('Sparks<span class="sr-only">: forged</span>')
  expect(h).toContain('Gone quiet: no activity for 5 business days')
  expect(h).toContain('data-testid="journey-t@x-f101"')
  expect(h).toContain('aria-pressed="true">By person')
})

test('by training: one panel for the training, everyone in it', () => {
  const h = page('?by=training')
  expect(h).toContain('aria-pressed="true">By training')
  expect(h.match(/class="panel journey-group"/g)).toHaveLength(1)
  expect(h).toContain('2 people')
})

test('filters come from the address: one person, the numbers follow, and a way back to everyone', () => {
  const h = page('?person=u@x')
  expect(h).toContain('<option value="u@x" selected="">Uma</option>')
  expect(h).not.toContain('aria-label="Tara"')
  expect(h).toContain('aria-label="Uma"')
  expect(h).toContain('<strong>1</strong> person')
  expect(h).toContain('<strong>100%</strong> forged on average')
  expect(h).toContain('Showing 1 of 2 lines.')
  expect(h).toContain('Clear filters')
  expect(page('?look=1&training=f101')).not.toContain('aria-label="Uma"') // Uma has nothing to look at
  expect(page()).not.toContain('Clear filters')
})
