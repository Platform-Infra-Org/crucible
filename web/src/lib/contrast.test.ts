/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { describe, expect, test } from 'vitest'
import { contrast } from './contrast'

const css = readFileSync(new URL('../theme/tokens.css', import.meta.url), 'utf8')
function tokens(theme: string): Record<string, string> {
  const block = css.split('}').find((b: string) => b.includes(`[data-theme='${theme}']`))!
  return Object.fromEntries([...block.matchAll(/--([\w-]+):\s*(#[0-9a-fA-F]{3,6})\b/g)].map((m) => [m[1], m[2]]))
}

// Text pairs need 4.5:1 (7:1 for High Contrast = AAA); focus rings and other UI need 3:1.
const textPairs = [['text', 'bg'], ['text', 'surface'], ['text', 'surface-2'], ['muted', 'bg'], ['muted', 'surface'], ['muted', 'surface-2'],
  ['accent', 'bg'], ['accent', 'surface'], ['accent-2', 'bg'], ['accent-2', 'surface'], ['danger', 'bg'], ['danger', 'surface'],
  ['ok', 'bg'], ['ok', 'surface'], ['term-fg', 'term-bg']]
const uiPairs = [['accent-2', 'bg'], ['accent-2', 'surface'], ['accent-2', 'surface-2'], ['accent', 'surface-2']]

describe('theme contrast (spec §12: contrast checked per theme; High Contrast = WCAG AAA)', () => {
  for (const theme of ['forge', 'anvil', 'quench', 'contrast']) {
    const t = tokens(theme)
    const min = theme === 'contrast' ? 7 : 4.5
    for (const [fg, bg] of textPairs) {
      test(`${theme}: ${fg} on ${bg} >= ${min}`, () => {
        expect(contrast(t[fg], t[bg])).toBeGreaterThanOrEqual(min)
      })
    }
    for (const [fg, bg] of uiPairs) {
      test(`${theme}: UI ${fg} on ${bg} >= 3`, () => {
        expect(contrast(t[fg], t[bg])).toBeGreaterThanOrEqual(3)
      })
    }
  }
  test('the formula', () => {
    expect(contrast('#000', '#fff')).toBeCloseTo(21, 0)
    expect(contrast('#777', '#fff')).toBeCloseTo(4.48, 1)
  })
})
