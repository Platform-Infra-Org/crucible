import { expect, test } from 'vitest'
import { unifiedDiff } from './diff'

test('a change reads like git', () => {
  const d = unifiedDiff('modules/m1/a.md', 'modules/m1/a.md', '# A\nold\n', '# A\nnew\n')
  expect(d).toContain('diff --git a/modules/m1/a.md b/modules/m1/a.md')
  expect(d).toContain('--- a/modules/m1/a.md')
  expect(d).toContain('+++ b/modules/m1/a.md')
  expect(d).toContain('-old')
  expect(d).toContain('+new')
  expect(d).not.toContain('====')
})

test('renames, additions and deletions', () => {
  expect(unifiedDiff('m/a.md', 'm/b.md', 'x\n', 'x\n')).toContain('rename from m/a.md\nrename to m/b.md')
  expect(unifiedDiff(undefined, 'm/n.md', '', 'hi\n')).toContain('--- /dev/null')
  expect(unifiedDiff('m/o.md', undefined, 'bye\n', '')).toContain('-bye')
})
