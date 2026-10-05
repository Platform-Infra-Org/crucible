import { useEffect, useRef } from 'react'
import { Terminal as XTerm } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'

function cssVar(name: string) {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim()
}

export function Terminal({ labId, name, tabKey, active, live, fontSize }: { labId: string; name: string; tabKey: string; active: boolean; live: boolean; fontSize: number }) {
  const el = useRef<HTMLDivElement>(null)
  const termRef = useRef<XTerm | null>(null)
  const fitRef = useRef<FitAddon | null>(null)
  const liveRef = useRef(live)
  liveRef.current = live
  useEffect(() => {
    if (!termRef.current) return
    termRef.current.options.fontSize = fontSize
    try { fitRef.current?.fit() } catch { /* hidden */ }
  }, [fontSize])
  useEffect(() => {
    const term = new XTerm({
      cursorBlink: true,
      fontFamily: "'JetBrains Mono', Menlo, monospace",
      fontSize,
      theme: { background: cssVar('--term-bg'), foreground: cssVar('--term-fg'), cursor: cssVar('--accent') },
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    termRef.current = term
    fitRef.current = fit
    term.open(el.current!)
    try { fit.fit() } catch { /* hidden tab: fitted when shown */ }
    let ws: WebSocket | null = null
    let closed = false
    let retry = 0
    let delay = 2000
    const connect = () => {
      const proto = location.protocol === 'https:' ? 'wss' : 'ws'
      ws = new WebSocket(`${proto}://${location.host}/api/labs/${labId}/terminals/${encodeURIComponent(name)}/ws?cols=${term.cols}&rows=${term.rows}`)
      ws.binaryType = 'arraybuffer'
      ws.onopen = () => { delay = 2000 }
      ws.onmessage = (e) => term.write(new Uint8Array(e.data as ArrayBuffer))
      ws.onclose = (e) => {
        if (closed) return
        const why = e.reason ? `: ${e.reason}` : ''
        if (!liveRef.current) {
          term.write(`\r\n\x1b[33m[forge] terminal closed${why}\x1b[0m\r\n`)
          return
        }
        term.write(`\r\n\x1b[33m[forge] connection lost${why}, retrying in ${Math.round(delay / 1000)}s…\x1b[0m\r\n`)
        retry = window.setTimeout(connect, delay)
        delay = Math.min(delay * 2, 15000)
      }
    }
    connect()
    const enc = new TextEncoder()
    const input = term.onData((d) => ws?.readyState === WebSocket.OPEN && ws.send(enc.encode(d)))
    const ro = new ResizeObserver(() => {
      if (!el.current?.offsetWidth) return
      fit.fit()
      if (ws?.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ cols: term.cols, rows: term.rows }))
    })
    ro.observe(el.current!)
    return () => {
      closed = true
      clearTimeout(retry)
      input.dispose()
      ro.disconnect()
      ws?.close()
      termRef.current = null
      fitRef.current = null
      term.dispose()
    }
  }, [labId, name])
  return <div ref={el} className="terminal" data-terminal={tabKey} style={{ display: active ? 'block' : 'none' }} />
}
