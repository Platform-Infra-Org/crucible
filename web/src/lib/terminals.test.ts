import { describe, expect, it } from 'vitest'
import { closeTab, openTab } from './terminals'

describe('terminal tabs', () => {
  it('names the first tab of a terminal after it, then numbers the copies', () => {
    let tabs: { key: string; name: string }[] = []
    tabs = [...tabs, openTab(tabs, 'console')]
    tabs = [...tabs, openTab(tabs, 'console')]
    expect(tabs.map((t) => t.key)).toEqual(['console', 'console #2'])
  })

  it('reuses the key a closed tab gave back, so a closed console reopens as itself', () => {
    const tabs = [{ key: 'workspace', name: 'workspace' }, { key: 'console', name: 'console' }]
    const after = closeTab(tabs, 'console')
    expect(after.tabs.map((t) => t.key)).toEqual(['workspace'])
    expect(after.active).toBe('workspace')
    expect(openTab(after.tabs, 'console').key).toBe('console')
  })

  it('shows the tab that took the closed one`s place', () => {
    const tabs = [{ key: 'a', name: 'a' }, { key: 'b', name: 'b' }, { key: 'c', name: 'c' }]
    expect(closeTab(tabs, 'b').active).toBe('c')
    expect(closeTab(tabs, 'c').active).toBe('b')
  })
})
