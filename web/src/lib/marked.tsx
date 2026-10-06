import type { ReactNode } from 'react'

const named: Record<string, string> = { '\r': '␍', '\t': '⇥' }
// Control, format (bidi, zero-width, BOM, tags), line/paragraph separators, fillers and variation selectors: none may
// hide from a reviewer. Tag characters and variation selectors are astral/BMP code points, so match by code point.
// eslint-disable-next-line no-misleading-character-class
const hidden = /[\p{Cc}\p{Cf}\p{Zl}\p{Zp}͏ᅟᅠㅤﾠ︀-️᠎￹-￻]|[\u{E0000}-\u{E007F}]|[\u{E0100}-\u{E01EF}]/u

// marked turns invisible characters into visible markers and trailing spaces into dots. Text goes through React as
// text, so nothing is parsed as HTML.
export function marked(line: string, trailing = true): ReactNode[] {
  const tail = trailing ? (/[ \t\r]+$/.exec(line)?.index ?? line.length) : line.length
  const out: ReactNode[] = []
  let text = ''
  const flush = () => { if (text) out.push(text); text = '' }
  let i = 0
  for (const c of line) {
    if (c === ' ' && i >= tail) {
      flush()
      out.push(<span key={i} className="diff-ws" title="trailing whitespace">·</span>)
    } else if (hidden.test(c)) {
      flush()
      const u = `U+${c.codePointAt(0)!.toString(16).toUpperCase().padStart(4, '0')}`
      out.push(<span key={i} className="diff-ctrl" title={u}>{named[c] ?? `‹${u}›`}</span>)
    } else text += c
    i += c.length
  }
  flush()
  return out
}
