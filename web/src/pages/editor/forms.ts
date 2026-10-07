import type { BlockField } from '../../types'
import { parseLab } from './previewModel'

export const initialValues = (fields: BlockField[]): Record<string, string> => Object.fromEntries(fields.map((f) => [f.name, f.default ?? '']))

const id = /^[A-Za-z0-9_][A-Za-z0-9_-]{0,62}$/

// formProblem mirrors blocks.checkValues so the form says what's wrong before the server does.
export function formProblem(fields: BlockField[], values: Record<string, string>): string | undefined {
  for (const f of fields) {
    const v = (values[f.name] ?? '').trim()
    if (!v) { if (f.required) return `${f.name} is required.`; continue }
    if (['id', 'module', 'task'].includes(f.type) && !id.test(v)) return `${f.name}: letters, digits, '-' and '_' only.`
    if ((f.type === 'string' || f.type === 'enum') && /[\r\n]/.test(v)) return `${f.name}: one line.`
    if (f.type === 'number' && (isNaN(Number(v)) || (f.min !== undefined && Number(v) < f.min) || (f.max !== undefined && Number(v) > f.max)))
      return `${f.name}: a number${f.min !== undefined ? ` from ${f.min}` : ''}${f.max !== undefined ? ` to ${f.max}` : ''}.`
    if (f.type === 'integer' && !/^\d+$/.test(v)) return `${f.name}: a whole number.`
    if (f.type === 'ints' && !/^\d+(\s*,\s*\d+)*$/.test(v)) return `${f.name}: numbers separated by commas, e.g. 0, 2.`
    if (f.type === 'pairs' && v.split('\n').some((l) => !l.includes('='))) return `${f.name}: one "left = right" per line.`
    if (f.type === 'path' && !/^[A-Za-z0-9_-]+(\/[A-Za-z0-9_-][A-Za-z0-9_.-]*)*\.sh$/.test(v)) return `${f.name}: a .sh path inside the lab folder.`
  }
}

export const modulesOf = (paths: string[]) =>
  paths.flatMap((p) => /^modules\/([^/]+)\/module\.yaml$/.exec(p)?.slice(1, 2) ?? []).sort()

// labOf is the module's lab.yaml in the draft (modules/<module>/<folder>/lab.yaml), if it has one.
export const labOf = (paths: string[], module: string) =>
  module ? paths.find((p) => p.startsWith(`modules/${module}/`) && /^[^/]+\/lab\.yaml$/.test(p.slice(`modules/${module}/`.length))) : undefined

export function tasksOf(paths: string[], module: string, read: (p: string) => string | undefined): string[] {
  const lab = labOf(paths, module)
  const text = lab ? read(lab) : undefined
  return text ? (parseLab(text).lab?.tasks ?? []).map((t) => t.id) : []
}
