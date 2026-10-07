import type { EditOp } from '../../types'

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

// Spec: a dark editor for Forge, Quench and High Contrast (high-contrast black there), light for Anvil.
export const monacoTheme = (theme: string | undefined) => (theme === 'anvil' ? 'vs' : theme === 'contrast' ? 'hc-black' : 'vs-dark')
