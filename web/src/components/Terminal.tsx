import { isLeaveChord } from '../lib/advanceFocus'
import { useEffect, useRef } from 'react'
import { Terminal as XTerm } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { fixedSize, terminalTheme } from '../lib/terminal'

function cssVar(name: string) {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim()
}

export function Terminal({ labId, name, size, tabKey, idx, onLeave, active, live, fontSize }: { labId: string; name: string; size?: string; tabKey: string; idx: number; onLeave: () => void; active: boolean; live: boolean; fontSize: number }) {
  const el = useRef<HTMLDivElement>(null)
  const termRef = useRef<XTerm | null>(null)
  const fitRef = useRef<FitAddon | null>(null)
  const liveRef = useRef(live)
  liveRef.current = live
  const leaveRef = useRef(onLeave)
  leaveRef.current = onLeave
  // A fixed-size terminal (lab.yaml size: 80x24, a serial console) never fits its panel: GRUB places its cursor on an
  // 80x24 screen, so the browser's grid must be exactly that.
  const fixed = fixedSize(size)
  useEffect(() => {
    if (!termRef.current) return
    termRef.current.options.fontSize = fontSize
    if (!fixed) try { fitRef.current?.fit() } catch { /* hidden */ }
  }, [fontSize, size]) // eslint-disable-line react-hooks/exhaustive-deps -- fixed follows size
  useEffect(() => {
    const term = new XTerm({
      cursorBlink: true,
      fontFamily: "'JetBrains Mono', Menlo, monospace",
      fontSize,
      theme: terminalTheme(cssVar),
      ...(fixed ?? {}),
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    termRef.current = term
    fitRef.current = fit
    term.open(el.current!)
    // xterm swallows Tab, so give keyboard users a way out (no focus trap).
    // Ctrl+C is SIGINT in a shell, so copy/paste is Ctrl+Shift+C/V as in Linux terminals; Cmd+C/V on a Mac stays native.
    term.attachCustomKeyEventHandler((e) => {
      if (e.type === 'keydown' && isLeaveChord(e)) { leaveRef.current(); return false }
      if (e.ctrlKey && e.shiftKey && !e.altKey && (e.code === 'KeyC' || e.code === 'KeyV')) {
        if (e.type === 'keydown') {
          e.preventDefault() // no browser paste on top of ours
          if (e.code === 'KeyC') { const sel = term.getSelection(); if (sel) navigator.clipboard?.writeText(sel).catch(() => {}) }
          else navigator.clipboard?.readText().then((t) => term.paste(t)).catch(() => {})
        }
        return false
      }
      return true
    })
    if (!fixed) try { fit.fit() } catch { /* hidden tab: fitted when shown */ }
    let ws: WebSocket | null = null
    let closed = false
    let retry = 0
    let delay = 2000
    const connect = () => {
      const proto = location.protocol === 'https:' ? 'wss' : 'ws'
      ws = new WebSocket(`${proto}://${location.host}/api/labs/${labId}/terminals/${encodeURIComponent(name)}/ws?cols=${term.cols}&rows=${term.rows}&tab=${encodeURIComponent(tabKey)}`)
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
      if (fixed || !el.current?.offsetWidth) return
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
    // eslint-disable-next-line react-hooks/exhaustive-deps -- the size comes with the terminal's name: a new size is a new terminal
  }, [labId, name, tabKey])
  return <div ref={el} role="tabpanel" id={`term-panel-${idx}`} aria-labelledby={`term-tab-${idx}`} className="terminal" data-terminal={tabKey} style={{ display: active ? 'block' : 'none' }} />
}
