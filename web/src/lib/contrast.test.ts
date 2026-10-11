/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { describe, expect, test } from 'vitest'
import { contrast } from './contrast'
import { THEMES } from '../theme/theme'

const css = readFileSync(new URL('../theme/tokens.css', import.meta.url), 'utf8')
function tokens(theme: string): Record<string, string> {
  const block = css.split('}').find((b: string) => b.includes(`[data-theme='${theme}']`))!
  return Object.fromEntries([...block.matchAll(/--([\w-]+):\s*(#[0-9a-fA-F]{3,6})\b/g)].map((m) => [m[1], m[2]]))
}

// Every colour/background pair app.css draws. Text pairs need 4.5:1 (7:1 for High Contrast = AAA); focus rings and
// other UI need 3:1. Surfaces: bg (page), surface (cards, nav, modals), surface-2 (code, badges, tabs), term-bg (terminal, diff).
const onSurfaces = ['bg', 'surface', 'surface-2']
const textPairs = [
  ...['text', 'muted', 'accent', 'accent-2', 'danger', 'ok'].flatMap((fg) => onSurfaces.map((bg) => [fg, bg])), // links, badges, hljs tokens, callouts, timers, .pass/.error/.warn
  ['term-fg', 'term-bg'], ['diff-add', 'term-bg'], ['diff-del', 'term-bg'], ['diff-hunk', 'term-bg'], // terminal and diff view
  ['on-accent', 'accent'], ['on-accent', 'accent-2'], // button.primary gradient
  ['bg', 'ok'], ['bg', 'danger'], ['bg', 'text'], // passed pip, button.danger, forged heat cell
]
const uiPairs = [...['accent', 'accent-2', 'ok', 'danger'].flatMap((fg) => onSurfaces.map((bg) => [fg, bg]))] // focus ring, state borders

describe('theme contrast (spec §12: contrast checked per theme; High Contrast = WCAG AAA)', () => {
  test('every theme in Settings has its own token block, with every token', () => {
    const want = Object.keys(tokens('forge'))
    for (const { id } of THEMES) expect(Object.keys(tokens(id)).sort(), id).toEqual([...want].sort())
  })
  for (const theme of THEMES.map((t) => t.id)) {
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
  test('app.css hard-codes no colour outside the tokens (a literal would dodge the pairs above)', () => {
    const app = readFileSync(new URL('../theme/app.css', import.meta.url), 'utf8')
    const bad = app.split('\n').filter((l) => /(^|[\s;{])(color|background(-color)?)\s*:[^;}]*(#[0-9a-fA-F]{3,8}\b|rgba?\(|hsla?\()/.test(l) && !/rgba\(0, 0, 0, 0\.55\)/.test(l))
    expect(bad).toEqual([])
  })
  test('the formula', () => {
    expect(contrast('#000', '#fff')).toBeCloseTo(21, 0)
    expect(contrast('#777', '#fff')).toBeCloseTo(4.48, 1)
  })
})
