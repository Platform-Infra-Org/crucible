import type { EditOp } from '../../types'
import { THEMES, isLight } from '../../theme/theme'

// DraftOps is a draft's changes in normal form: base paths renamed (current path → base path), base paths deleted, and
// the text of every new or changed file at its current path. toOps emits renames, deletes, puts: the order
// gitsync.ApplyOps applies them in.
export type DraftOps = { renames: Record<string, string>; deletes: string[]; puts: Record<string, string> }
export type Change = { kind: 'added' | 'changed' | 'renamed' | 'deleted'; path: string; from?: string }

export const emptyOps = (): DraftOps => ({ renames: {}, deletes: [], puts: {} })

export function fromOps(ops: EditOp[]): DraftOps {
  const d = emptyOps()
  for (const op of ops) {
    if (op.op === 'rename') d.renames[op.to] = op.from
    else if (op.op === 'delete') d.deletes.push(op.path)
    else d.puts[op.path] = op.content ?? ''
  }
  return d
}

const byKey = <T,>(a: [string, T], b: [string, T]) => a[0].localeCompare(b[0])

export function toOps(d: DraftOps): EditOp[] {
  return [
    ...Object.entries(d.renames).sort(byKey).map(([to, from]) => ({ op: 'rename' as const, from, to })),
    ...[...d.deletes].sort().map((path) => ({ op: 'delete' as const, path })),
    ...Object.entries(d.puts).sort(byKey).map(([path, content]) => ({ op: 'put' as const, path, content })),
  ]
}

const movedAway = (d: DraftOps, p: string) => Object.values(d.renames).includes(p)

// origin is the base path whose text `path` starts from; undefined for a file new in this draft.
export function origin(base: string[], d: DraftOps, path: string): string | undefined {
  if (path in d.renames) return d.renames[path]
  return base.includes(path) && !movedAway(d, path) && !d.deletes.includes(path) ? path : undefined
}

// fileText is path's text in the draft: its put, else its base text. undefined while that base text isn't loaded (or the
// path isn't in the draft): never "", which the next keystroke would save as the whole file.
export function fileText(base: string[], d: DraftOps, baseText: Record<string, string>, path: string): string | undefined {
  if (path in d.puts) return d.puts[path]
  const o = origin(base, d, path)
  return o === undefined ? undefined : baseText[o]
}

export function currentPaths(base: string[], d: DraftOps): string[] {
  const out = new Set(base.filter((p) => !movedAway(d, p) && !d.deletes.includes(p)))
  for (const p of Object.keys(d.renames)) out.add(p)
  for (const p of Object.keys(d.puts)) out.add(p)
  return [...out].sort()
}

// putText records path's text. original is the text the file starts from (undefined for a new file): a file edited
// back to its original leaves the draft.
// A put at a path this draft deleted un-deletes it: the server refuses delete+put of one path.
export function putText(d: DraftOps, path: string, text: string, original: string | undefined): DraftOps {
  if (d.deletes.includes(path)) return { ...d, deletes: d.deletes.filter((p) => p !== path), puts: { ...d.puts, [path]: text } }
  const puts = { ...d.puts }
  if (original !== undefined && text === original) delete puts[path]
  else puts[path] = text
  return { ...d, puts }
}

export function renamePath(base: string[], d: DraftOps, from: string, to: string): DraftOps {
  if (from === to) return d
  if (currentPaths(base, d).includes(to)) throw new Error(`${to} already exists`)
  if (d.deletes.includes(to)) throw new Error(`${to} was deleted in this draft; pick another name`)
  const o = origin(base, d, from)
  const renames = { ...d.renames }
  delete renames[from]
  if (o !== undefined && o !== to) renames[to] = o
  const puts = { ...d.puts }
  if (from in puts) {
    puts[to] = puts[from]
    delete puts[from]
  }
  return { ...d, renames, puts }
}

export function deletePath(base: string[], d: DraftOps, path: string): DraftOps {
  const o = origin(base, d, path)
  const renames = { ...d.renames }
  const puts = { ...d.puts }
  delete renames[path]
  delete puts[path]
  return { renames, puts, deletes: o === undefined ? d.deletes : [...d.deletes, o] }
}

// applyInsert applies an insert's puts, which the server computed from the draft `sent`, to the draft as it is `now`.
// If a file the insert writes was typed in, deleted or renamed since, applying would undo that work: it returns the
// file instead and changes nothing. originals are base texts by base path (a put equal to its original leaves the draft).
export function applyInsert(base: string[], sent: DraftOps, now: DraftOps, puts: { path: string; content: string }[],
  originals: Record<string, string | undefined>): { next: DraftOps } | { changed: string } {
  const moved = puts.find((o) => origin(base, sent, o.path) !== origin(base, now, o.path) || sent.puts[o.path] !== now.puts[o.path])
  if (moved) return { changed: moved.path }
  return { next: puts.reduce((d, o) => { const b = origin(base, d, o.path); return putText(d, o.path, o.content, b === undefined ? undefined : originals[b]) }, now) }
}

export function changeList(base: string[], d: DraftOps): Change[] {
  const out: Change[] = []
  for (const [to, from] of Object.entries(d.renames)) out.push({ kind: 'renamed', path: to, from })
  for (const p of d.deletes) out.push({ kind: 'deleted', path: p })
  for (const p of Object.keys(d.puts)) {
    if (!(p in d.renames)) out.push({ kind: origin(base, d, p) === undefined ? 'added' : 'changed', path: p })
  }
  return out.sort((a, b) => a.path.localeCompare(b.path))
}

// matchFiles is "go to file": the letters of q in order, files whose name holds q as a run first, then shorter paths.
export function matchFiles(paths: string[], q: string): string[] {
  const s = q.toLowerCase()
  if (!s) return paths
  const inOrder = (p: string) => { let i = 0; for (const c of p.toLowerCase()) if (c === s[i]) i++; return i === s.length }
  const name = (p: string) => p.slice(p.lastIndexOf('/') + 1).toLowerCase()
  return paths.filter(inOrder).sort((a, b) => Number(name(b).includes(s)) - Number(name(a).includes(s)) || a.length - b.length)
}

export const languageOf = (path: string) =>
  path.endsWith('.md') ? 'markdown' : path.endsWith('.sh') ? 'shell' : /\.ya?ml$/.test(path) ? 'yaml' : 'plaintext'

// monacoTheme names the editor's Monaco theme for an app theme; monaco.ts defines it from monacoThemeData.
export const monacoTheme = (theme: string | undefined) => `crucible-${THEMES.some((t) => t.id === theme) ? theme : 'forge'}`

// toHex turns a CSS colour as getComputedStyle gives it (#rgb[a], #rrggbb[aa], rgb()/rgba()) into the #rrggbb[aa] Monaco
// needs; anything else is undefined.
export function toHex(css: string): string | undefined {
  const s = css.trim().toLowerCase()
  if (/^#([0-9a-f]{3,4})$/.test(s)) return '#' + [...s.slice(1)].map((c) => c + c).join('')
  if (/^#([0-9a-f]{6}|[0-9a-f]{8})$/.test(s)) return s
  const m = /^rgba?\(\s*([\d.]+)[\s,]+([\d.]+)[\s,]+([\d.]+)\s*(?:[,/]\s*([\d.]+)(%?))?\s*\)$/.exec(s)
  if (!m) return undefined
  const byte = (n: number) => Math.round(Math.min(255, Math.max(0, n))).toString(16).padStart(2, '0')
  const a = m[4] === undefined ? '' : byte((m[5] ? Number(m[4]) / 100 : Number(m[4])) * 255)
  return '#' + byte(+m[1]) + byte(+m[2]) + byte(+m[3]) + a
}

type ThemeData = { base: 'vs' | 'vs-dark' | 'hc-black'; inherit: true; colors: Record<string, string>; rules: { token: string; foreground: string; fontStyle?: string }[] }

// monacoThemeData builds the Monaco theme for an app theme from its CSS tokens (tok reads one, e.g. '--accent'), so
// tokens.css stays the one place colours live. Light themes get Monaco's light base, High Contrast its high-contrast one.
export function monacoThemeData(theme: string | undefined, tok: (name: string) => string): ThemeData {
  const c = (name: string, alpha = '') => { const h = toHex(tok(name)); return h && (h.length === 7 ? h + alpha : h) }
  const colors: Record<string, string | undefined> = {
    'editor.background': c('--bg'), 'editor.foreground': c('--text'), 'editorGutter.background': c('--bg'),
    'editorLineNumber.foreground': c('--muted'), 'editorLineNumber.activeForeground': c('--accent'), 'editorCursor.foreground': c('--accent'),
    'editor.selectionBackground': c('--accent', '40'), 'editor.inactiveSelectionBackground': c('--accent', '26'),
    'editor.selectionHighlightBackground': c('--accent', '26'), 'editor.wordHighlightBackground': c('--accent', '26'),
    'editor.wordHighlightStrongBackground': c('--accent', '33'), 'editor.findMatchBackground': c('--accent-2', '66'),
    'editor.findMatchHighlightBackground': c('--accent-2', '33'), 'editorBracketMatch.background': c('--accent', '26'), 'editorBracketMatch.border': c('--accent'),
    'editorIndentGuide.background1': c('--border'), 'editorIndentGuide.activeBackground1': c('--muted'), 'editorWhitespace.foreground': c('--border'),
    'editorWidget.background': c('--surface'), 'editorWidget.foreground': c('--text'), 'editorWidget.border': c('--border'),
    'editorHoverWidget.background': c('--surface'), 'editorHoverWidget.foreground': c('--text'), 'editorHoverWidget.border': c('--border'),
    'editorSuggestWidget.background': c('--surface'), 'editorSuggestWidget.foreground': c('--text'), 'editorSuggestWidget.border': c('--border'),
    'editorSuggestWidget.selectedBackground': c('--surface-2'), 'editorSuggestWidget.highlightForeground': c('--accent'),
    'quickInput.background': c('--surface'), 'quickInput.foreground': c('--text'), 'list.highlightForeground': c('--accent'),
    'list.hoverBackground': c('--surface-2'), 'list.activeSelectionBackground': c('--surface-2'), 'list.activeSelectionForeground': c('--text'),
    'list.focusBackground': c('--surface-2'), 'list.focusForeground': c('--text'), 'focusBorder': c('--accent-2'),
    'input.background': c('--surface-2'), 'input.foreground': c('--text'), 'input.border': c('--border'),
    'textLink.foreground': c('--accent'), 'textLink.activeForeground': c('--accent-2'), 'textCodeBlock.background': c('--surface-2'),
    'scrollbarSlider.background': c('--muted', '33'), 'scrollbarSlider.hoverBackground': c('--muted', '55'), 'scrollbarSlider.activeBackground': c('--muted', '77'),
    'editorOverviewRuler.border': c('--border'), 'editorError.foreground': c('--danger'), 'editorWarning.foreground': c('--accent-2'),
    'diffEditor.insertedTextBackground': c('--diff-add', '33'), 'diffEditor.removedTextBackground': c('--diff-del', '33'),
    'diffEditor.insertedLineBackground': c('--diff-add', '1a'), 'diffEditor.removedLineBackground': c('--diff-del', '1a'),
    'diffEditor.border': c('--border'),
    // High Contrast marks the current line with its own bright border; the others get a soft band.
    ...(theme === 'contrast' ? {} : { 'editor.lineHighlightBackground': c('--surface-2'), 'editor.lineHighlightBorder': c('--surface-2') }),
  }
  const fg = (name: string) => toHex(tok(name))?.slice(1, 7)
  const rules = ([
    ['comment', fg('--muted'), 'italic'], ['type', fg('--accent-2')], ['tag', fg('--accent-2')], ['variable', fg('--accent-2')],
    ['string.link', fg('--accent-2')], ['string', fg('--text')], ['string.yaml', fg('--text')],
    ['keyword', fg('--accent')], ['keyword.flow', fg('--accent')], ['number', fg('--accent')], ['number.hex', fg('--accent')],
  ] as const).flatMap(([token, foreground, fontStyle]) => (foreground ? [fontStyle ? { token, foreground, fontStyle } : { token, foreground }] : []))
  return {
    base: isLight(theme) ? 'vs' : theme === 'contrast' ? 'hc-black' : 'vs-dark', inherit: true, rules,
    colors: Object.fromEntries(Object.entries(colors).filter((e): e is [string, string] => !!e[1])),
  }
}
