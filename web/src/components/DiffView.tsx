import type { ReactNode } from 'react'

const kind = (l: string) =>
  l.startsWith('@@') ? 'diff-hunk' : l.startsWith('+') && !l.startsWith('+++') ? 'diff-add' : l.startsWith('-') && !l.startsWith('---') ? 'diff-del' : 'diff-ctx'

const named: Record<string, string> = { '\r': '␍', '\t': '⇥' }
// control, DEL/C1, zero-width, bidi and BOM characters: none may hide from a reviewer
// eslint-disable-next-line no-control-regex
const hidden = /[\u0000-\u0008\u000b-\u001f\u007f-\u009f\t\u00ad\u200b-\u200f\u2028\u2029\u202a-\u202e\u2060-\u2069\ufeff]/g

// marked turns invisible characters into visible markers and trailing spaces into dots. Text goes through React as
// text, so nothing is parsed as HTML.
export function marked(line: string, trailing = true): ReactNode[] {
  const tail = trailing ? (/[ \t\r]+$/.exec(line)?.index ?? line.length) : line.length
  const out: ReactNode[] = []
  let text = ''
  const flush = () => { if (text) out.push(text); text = '' }
  for (let i = 0; i < line.length; i++) {
    const c = line[i]
    if (c === ' ' && i >= tail) {
      flush()
      out.push(<span key={i} className="diff-ws" title="trailing whitespace">·</span>)
    } else if (new RegExp(hidden.source).test(c)) {
      flush()
      const u = `U+${c.charCodeAt(0).toString(16).toUpperCase().padStart(4, '0')}`
      out.push(<span key={i} className="diff-ctrl" title={u}>{named[c] ?? `‹${u}›`}</span>)
    } else text += c
  }
  flush()
  return out
}

export function DiffView({ diff }: { diff: string }) {
  return (
    <pre className="diff" data-testid="diff" aria-label="Changes">
      {diff.split('\n').map((l, i) => <div key={i} className={kind(l)}>{l === '' ? ' ' : marked(l, l !== ' ')}</div>)}
    </pre>
  )
}
