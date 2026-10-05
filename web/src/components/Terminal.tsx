import { useEffect, useRef } from 'react'
import { Terminal as XTerm } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'

function cssVar(name: string) {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim()
}

export function Terminal({ labId, name, tabKey, active }: { labId: string; name: string; tabKey: string; active: boolean }) {
  const el = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const term = new XTerm({
      cursorBlink: true,
      fontFamily: "'JetBrains Mono', Menlo, monospace",
      fontSize: 14,
      theme: { background: cssVar('--term-bg'), foreground: cssVar('--term-fg'), cursor: cssVar('--accent') },
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    term.open(el.current!)
    try { fit.fit() } catch { /* hidden tab: fitted when shown */ }
    let ws: WebSocket | null = null
    let closed = false
    let retry = 0
    const connect = () => {
      const proto = location.protocol === 'https:' ? 'wss' : 'ws'
      ws = new WebSocket(`${proto}://${location.host}/api/labs/${labId}/terminals/${encodeURIComponent(name)}/ws?cols=${term.cols}&rows=${term.rows}`)
      ws.binaryType = 'arraybuffer'
      ws.onmessage = (e) => term.write(new Uint8Array(e.data as ArrayBuffer))
      ws.onclose = () => {
        if (closed) return
        term.write('\r\n\x1b[33m[forge] connection lost, reconnecting…\x1b[0m\r\n')
        retry = window.setTimeout(connect, 2000)
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
      term.dispose()
    }
  }, [labId, name])
  return <div ref={el} className="terminal" data-terminal={tabKey} style={{ display: active ? 'block' : 'none' }} />
}
