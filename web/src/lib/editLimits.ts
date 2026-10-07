import type { EditOp } from '../types'
import { byteLen } from './editDraft'

// Client-side mirror of gitsync's edit limits; the server stays the authority.
export const MAX_EDIT_FILES = 20
export const MAX_EDIT_FILE_BYTES = 256 << 10
const exts = ['.md', '.yaml', '.yml', '.sh']

// The server reads at most 1 MiB per request; this leaves room for the JSON around the ops.
export const MAX_DRAFT_BYTES = 1_000_000
const part = /^[A-Za-z0-9_-]([A-Za-z0-9._-]{0,98}[A-Za-z0-9_-])?$/

// pathProblem mirrors gitsync.CheckPath; the server stays the authority.
export function pathProblem(p: string): string | undefined {
  if (p !== 'training.yaml' && !/^modules\/[^/]+\/.+/.test(p)) return `${p}: only training.yaml and files under modules/<id>/ can be edited here.`
  if (p.length > 255 || !p.split('/').every((x) => part.test(x))) return `${p}: use letters, digits, '.', '_' and '-' in each part, not starting or ending with '.'.`
  if (!exts.some((e) => p.toLowerCase().endsWith(e))) return `${p}: only .md, .yaml, .yml and .sh files can be edited here.`
}

// opsProblem mirrors gitsync.CheckOps for drafts (no lower bound: an empty draft is fine) plus the 1 MiB request cap.
export function opsProblem(ops: EditOp[]): string | undefined {
  if (ops.length > MAX_EDIT_FILES) return `A draft changes at most ${MAX_EDIT_FILES} files.`
  for (const op of ops) {
    for (const p of op.op === 'rename' ? [op.from, op.to] : [op.path]) {
      const bad = pathProblem(p)
      if (bad) return bad
    }
    if ((op.op === 'delete' && op.path === 'training.yaml') || (op.op === 'rename' && (op.from === 'training.yaml' || op.to === 'training.yaml')))
      return "training.yaml can't be renamed or deleted."
    if (op.op === 'put' && (byteLen(op.content) > MAX_EDIT_FILE_BYTES || op.content.includes('\0'))) return `${op.path}: must be text of at most 256 KiB.`
  }
  if (byteLen(JSON.stringify(ops)) > MAX_DRAFT_BYTES) return 'This draft is over 1 MiB. Split it into smaller drafts.'
}
