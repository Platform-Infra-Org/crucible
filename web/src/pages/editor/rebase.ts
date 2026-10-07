import type { Conflict, EditOp } from '../../types'

export type Choice = 'drop' | 'keep' | { put: string }

const same = (a: EditOp, b: EditOp) => JSON.stringify(a) === JSON.stringify(b)

// canKeep: a put can always be kept (it recreates a file deleted upstream); a rename or delete needs its source to
// still exist upstream, and a rename can't land on a path that appeared upstream.
export function canKeep(c: Conflict): boolean {
  if (c.op.op === 'put') return true
  if (c.head_missing) return false
  return !(c.op.op === 'rename' && c.path === c.op.to)
}

// resolveOps applies the author's choice for each conflict to the draft's ops; ops without a conflict stay as they are.
// One op can have two conflicts (a rename whose source changed and whose target appeared): dropping either drops it.
// Dropping a rename (c→d) moves the kept edit of its new name back to c, so it neither leaves a copy at d nor is lost.
// It stays at d, as a new file, when c is gone upstream or the draft writes c again.
export function resolveOps(ops: EditOp[], conflicts: Conflict[], choices: Choice[]): EditOp[] {
  const kept = ops.flatMap((op) => {
    const mine = choices.filter((_, i) => same(conflicts[i].op, op))
    if (mine.includes('drop')) return []
    const put = mine.findLast((ch): ch is { put: string } => typeof ch === 'object')
    return op.op === 'put' && put ? [{ ...op, content: put.put }] : [op]
  })
  const back = new Map<string, string>() // dropped rename's target → its source, where the target's edit goes
  for (const op of ops) {
    if (op.op !== 'rename' || kept.includes(op)) continue
    const sourceGone = conflicts.some((c) => same(c.op, op) && c.path === op.from && c.head_missing)
    const rewritten = kept.some((k) => k.op === 'put' && k.path === op.from)
    if (!sourceGone && !rewritten) back.set(op.to, op.from)
  }
  return kept.map((op) => (op.op === 'put' && back.has(op.path) ? { ...op, path: back.get(op.path)! } : op))
}

// BaseTexts are the draft's original file texts, all from one base commit.
export type BaseTexts = { sha: string; text: Record<string, string> }

// withBase adds a fetched base text, unless the draft moved to another base while it was in flight (a rebase or reload).
export const withBase = (b: BaseTexts, sha: string, path: string, content: string): BaseTexts =>
  b.sha === sha ? { sha, text: { ...b.text, [path]: content } } : b
