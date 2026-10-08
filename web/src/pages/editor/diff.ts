import { createTwoFilesPatch } from 'diff'

// unifiedDiff renders one file's change the way git does, so the Changes panel can reuse the reviewers' DiffView
// (which marks hidden and control characters). undefined from/to means the file is added/deleted.
export function unifiedDiff(from: string | undefined, to: string | undefined, before: string, after: string): string {
  const head = `diff --git a/${from ?? to} b/${to ?? from}\n` + (from && to && from !== to ? `rename from ${from}\nrename to ${to}\n` : '')
  const patch = createTwoFilesPatch(from ? `a/${from}` : '/dev/null', to ? `b/${to}` : '/dev/null', before, after, undefined, undefined, { context: 3 })
  return head + patch.split('\n').filter((l) => !l.startsWith('====') && !l.startsWith('Index: ')).join('\n')
}
