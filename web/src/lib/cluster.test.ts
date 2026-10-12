import { describe, expect, test } from 'vitest'
import { clusterReady, keepAsking, keepPick, pickContext } from './cluster'

describe('picking a cluster', () => {
  const cs = [{ name: 'kind-dev', server: '127.0.0.1:1' }, { name: 'prod-eks', server: 'x.eks.amazonaws.com' }]
  test('the last choice when it is still there, else nothing chosen', () => {
    expect(pickContext(cs, 'prod-eks')).toBe('prod-eks')
    expect(pickContext(cs, 'gone')).toBe('')
    expect(pickContext([cs[0]], null)).toBe('kind-dev') // one context: take it
    expect(pickContext(cs, null)).toBe('')
  })
})

describe('the lab page and the agent', () => {
  const cs = [{ name: 'kind-dev', server: '127.0.0.1:1' }, { name: 'prod-eks', server: 'x.eks.amazonaws.com' }]
  test('a pick holds while the agent reports it; a vanished one un-picks', () => {
    expect(keepPick(cs, 'prod-eks', null)).toBe('prod-eks')
    expect(keepPick([cs[0]], 'prod-eks', null)).toBe('kind-dev') // gone: preselect again (the only one)
    expect(keepPick(cs, 'gone', null)).toBe('')
  })
  test('a cluster lab can start only on a context the agent reports, and a cluster-wide one with consent', () => {
    expect(clusterReady(cs, 'kind-dev', false, false)).toBe(true)
    expect(clusterReady([cs[1]], 'kind-dev', false, false)).toBe(false)
    expect(clusterReady(cs, '', false, false)).toBe(false)
    expect(clusterReady(cs, 'kind-dev', true, false)).toBe(false)
    expect(clusterReady(cs, 'kind-dev', true, true)).toBe(true)
  })
  test('the page asks again while the agent is away, or a cluster lab has nothing usable to pick', () => {
    expect(keepAsking({ runtime: 'local', runtime_ready: false }, '')).toBe(true)
    expect(keepAsking({ runtime: 'local', runtime_ready: true }, '')).toBe(false)
    expect(keepAsking({ runtime: 'cluster', runtime_ready: true, contexts: [] }, '')).toBe(true) // the agent said hello before its contexts
    expect(keepAsking({ runtime: 'cluster', runtime_ready: true, contexts: cs }, '')).toBe(true) // nothing picked yet: a new one may appear
    expect(keepAsking({ runtime: 'cluster', runtime_ready: true, contexts: cs }, 'kind-dev')).toBe(false)
  })
})
