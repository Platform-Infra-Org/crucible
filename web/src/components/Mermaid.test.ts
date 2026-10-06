import { expect, test } from 'vitest'
import { mermaidConfig } from './Mermaid'

test('directives cannot change the hardening', () => {
  const c = mermaidConfig(true)
  expect(c.securityLevel).toBe('strict')
  expect(c.flowchart.htmlLabels).toBe(false)
  for (const k of ['secure', 'securityLevel', 'startOnLoad', 'maxTextSize', 'htmlLabels', 'flowchart', 'themeCSS', 'themeVariables', 'fontFamily'])
    expect(c.secure).toContain(k)
})
