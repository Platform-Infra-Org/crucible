import type { ITheme } from '@xterm/xterm'

// fixedSize: a lab terminal's `size: COLSxROWS`, in the bounds the server accepts (content.Terminal.Fixed), else null
// (the terminal fits its panel).
export function fixedSize(size?: string): { cols: number; rows: number } | null {
  const m = /^([0-9]{2,3})x([0-9]{1,3})$/.exec(size ?? '')
  if (!m) return null
  const cols = Number(m[1]), rows = Number(m[2])
  return cols >= 40 && cols <= 250 && rows >= 10 && rows <= 100 ? { cols, rows } : null
}

// terminalTheme: the theme's terminal colours. ANSI black is the background, so a program that paints with it (GRUB on
// a serial console) looks like the terminal, not like xterm.js's default palette black (#2e3436).
export function terminalTheme(css: (name: string) => string): ITheme {
  const bg = css('--term-bg'), fg = css('--term-fg')
  return { background: bg, foreground: fg, cursor: fg, black: bg }
}
