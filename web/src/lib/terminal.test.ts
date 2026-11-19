import { expect, test } from 'vitest'
import { fixedSize, terminalTheme } from './terminal'

test('a terminal size is COLSxROWS within the bounds the server accepts', () => {
  expect(fixedSize('80x24')).toEqual({ cols: 80, rows: 24 })
  expect(fixedSize('132x43')).toEqual({ cols: 132, rows: 43 })
  for (const bad of [undefined, '', '80by24', '10x2', '999x999', '80x24 ']) expect(fixedSize(bad)).toBeNull()
})

// GRUB on a serial console paints the screen with ANSI black: it must be the terminal's own background, not
// xterm.js's default palette black (#2e3436), or the whole console turns slate as if selected.
test('the terminal theme makes ANSI black the background', () => {
  const css = (n: string) => ({ '--term-bg': '#0f0c0a', '--term-fg': '#f3e9df' })[n] ?? ''
  const t = terminalTheme(css)
  expect(t).toMatchObject({ background: '#0f0c0a', foreground: '#f3e9df', cursor: '#f3e9df', black: '#0f0c0a' })
})
