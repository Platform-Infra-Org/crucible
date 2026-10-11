import { expect, test, vi } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router'
import { LabPage } from './Lab'
import type { ModuleLab } from '../types'

const info = { title: 'Cluster heat', runtime: 'cluster', runtime_ready: false, runtime_message: 'Your laptop agent is offline.', lab: null, contexts: [] } as unknown as ModuleLab
vi.mock('../useFetch', () => ({ useFetch: () => ({ data: info, error: undefined }) }))

test('a cluster lab waiting on the agent links to Connect your laptop', () => {
  const h = renderToStaticMarkup(<MemoryRouter><LabPage /></MemoryRouter>)
  expect(h).toContain('Your laptop agent is offline.')
  expect(h).toContain('href="/connect"')
})
