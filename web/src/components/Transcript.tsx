import { useEffect, useRef } from 'react'
import { Terminal as XTerm } from '@xterm/xterm'

function cssVar(name: string) {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim()
}

// Replays (fetched as text, written to xterm; never HTML) a recorded terminal session (raw PTY bytes, colours and all) in a terminal nobody can type into.
export function TranscriptView({ labId, id }: { labId: string; id: number }) {
  const el = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const term = new XTerm({
      disableStdin: true, cursorBlink: false, scrollback: 20000, rows: 24, fontSize: 13,
      fontFamily: "'JetBrains Mono', Menlo, monospace",
      theme: { background: cssVar('--term-bg'), foreground: cssVar('--term-fg') },
    })
    term.open(el.current!)
    let gone = false
    fetch(`/api/labs/${labId}/transcripts/${id}`, { credentials: 'same-origin' })
      .then((r) => (r.ok ? r.text() : Promise.reject(new Error(`HTTP ${r.status}`))))
      .then((b) => { if (!gone) term.write(b) })
      .catch((e: Error) => { if (!gone) term.write(`[transcript unavailable: ${e.message}]`) })
    return () => { gone = true; term.dispose() }
  }, [labId, id])
  return <div ref={el} className="terminal transcript" data-testid={`transcript-${id}`} />
}
