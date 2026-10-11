import { afterEach, expect, test, vi } from 'vitest'
import { lastLook, rememberLook } from './theme'

const storage = () => {
  const m = new Map<string, string>()
  return { getItem: (k: string) => m.get(k) ?? null, setItem: (k: string, v: string) => void m.set(k, v) }
}
afterEach(() => vi.unstubAllGlobals())

test('the gate gets back the theme and calm setting the last person here used', () => {
  vi.stubGlobal('localStorage', storage())
  expect(lastLook()).toBeUndefined() // nobody yet: the instance default
  rememberLook('parchment', true)
  expect(lastLook()).toEqual({ theme: 'parchment', calm: true })
  rememberLook('starmetal', false)
  expect(lastLook()).toEqual({ theme: 'starmetal', calm: false })
})

test('anything unusable falls back to the instance default', () => {
  const s = storage()
  vi.stubGlobal('localStorage', s)
  s.setItem('crucible-look', '{"theme":"neon","calm":false}') // a theme that no longer exists
  expect(lastLook()).toBeUndefined()
  s.setItem('crucible-look', 'not json')
  expect(lastLook()).toBeUndefined()
  vi.stubGlobal('localStorage', { getItem: () => { throw new Error('blocked') }, setItem: () => { throw new Error('blocked') } })
  expect(lastLook()).toBeUndefined()
  expect(() => rememberLook('forge', false)).not.toThrow() // storage blocked: just not remembered
})
