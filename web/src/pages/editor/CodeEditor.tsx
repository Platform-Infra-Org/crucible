import { useEffect, useRef } from 'react'
import { monaco } from './monaco'
import { isLeaveChord } from '../../lib/advanceFocus'
import { languageOf, monacoTheme } from './model'

type Props = {
  draftId: number; path: string; text: string; readOnly: boolean
  markers: { line: number; message: string }[]; reveal?: { line: number; n: number }
  onChange: (path: string, text: string) => void; onLeave: () => void; onGoToFile: () => void
}

const uriOf = (draftId: number, path: string) => monaco.Uri.parse(`file:///draft-${draftId}/${path}`)
const pathOf = (m: monaco.editor.ITextModel) => m.uri.path.split('/').slice(2).join('/')

// CodeEditor is one Monaco editor; each open file is its own model, so undo history and cursor survive tab switches.
// A model keeps the file's line endings (CRLF stays CRLF).
export function CodeEditor({ draftId, path, text, readOnly, markers, reveal, onChange, onLeave, onGoToFile }: Props) {
  const host = useRef<HTMLDivElement>(null)
  const ed = useRef<monaco.editor.IStandaloneCodeEditor | null>(null)
  const cb = useRef({ onChange, onLeave, onGoToFile })
  useEffect(() => { cb.current = { onChange, onLeave, onGoToFile } })
  useEffect(() => {
    const e = monaco.editor.create(host.current!, {
      automaticLayout: true, accessibilitySupport: 'on', minimap: { enabled: false }, wordWrap: 'on',
      fontFamily: "'JetBrains Mono', monospace", theme: monacoTheme(document.documentElement.dataset.theme),
      renderControlCharacters: true, unicodeHighlight: { invisibleCharacters: true, ambiguousCharacters: true },
      ariaLabel: 'File editor. Press Control+Shift+F6 to leave.',
    })
    e.onKeyDown((k) => {
      if (isLeaveChord(k.browserEvent)) {
        k.preventDefault()
        k.stopPropagation()
        cb.current.onLeave()
      }
    })
    e.addCommand(monaco.KeyMod.CtrlCmd | monaco.KeyCode.KeyP, () => cb.current.onGoToFile())
    e.addCommand(monaco.KeyMod.CtrlCmd | monaco.KeyMod.Shift | monaco.KeyCode.KeyP, () => e.trigger('keyboard', 'editor.action.quickCommand', null))
    e.onDidChangeModelContent(() => {
      const m = e.getModel()
      if (m) cb.current.onChange(pathOf(m), m.getValue())
    })
    ed.current = e
    return () => {
      e.dispose()
      for (const m of monaco.editor.getModels()) if (m.uri.path.startsWith(`/draft-${draftId}/`)) m.dispose()
    }
  }, [draftId])
  useEffect(() => {
    const uri = uriOf(draftId, path)
    const m = monaco.editor.getModel(uri) ?? monaco.editor.createModel(text, languageOf(path), uri)
    if (m.getValue() !== text) m.setValue(text) // changed outside the editor (an insert, a rebase)
    if (ed.current?.getModel() !== m) ed.current?.setModel(m)
  }, [draftId, path, text])
  useEffect(() => { ed.current?.updateOptions({ readOnly }) }, [readOnly])
  useEffect(() => {
    const m = ed.current?.getModel()
    if (m) monaco.editor.setModelMarkers(m, 'crucible', markers.map((x) => ({
      severity: monaco.MarkerSeverity.Error, message: x.message, startLineNumber: x.line, startColumn: 1, endLineNumber: x.line, endColumn: m.getLineMaxColumn(Math.min(x.line, m.getLineCount())),
    })))
  }, [markers, path])
  useEffect(() => {
    if (!reveal || !ed.current) return
    ed.current.revealLineInCenter(reveal.line)
    ed.current.setPosition({ lineNumber: reveal.line, column: 1 })
    ed.current.focus()
  }, [reveal])
  return <div ref={host} className="code-editor" data-testid="code-editor" />
}
