import { describe, expect, test } from 'vitest'
import { pickContext } from './cluster'

describe('picking a cluster', () => {
  const cs = [{ name: 'kind-dev', server: '127.0.0.1:1' }, { name: 'prod-eks', server: 'x.eks.amazonaws.com' }]
  test('the last choice when it is still there, else nothing chosen', () => {
    expect(pickContext(cs, 'prod-eks')).toBe('prod-eks')
    expect(pickContext(cs, 'gone')).toBe('')
    expect(pickContext([cs[0]], null)).toBe('kind-dev') // one context: take it
    expect(pickContext(cs, null)).toBe('')
  })
})
