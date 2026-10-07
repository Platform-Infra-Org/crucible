import { expect, test } from 'vitest'
import { ancestors, buildTree, dirOf, iconOf, menuFor, rows, treeKey } from './tree'

const tree = buildTree(
  ['training.yaml', 'modules/02-b/module.yaml', 'modules/01-a/reading/b.md', 'modules/01-a/reading/a.md', 'modules/01-a/module.yaml', 'modules/10-c/lab/setup.sh'],
  [{ path: 'modules/01-a/lab/main.tf', reason: 'only .md, .yaml, .yml and .sh files can be edited here' }],
)
const flat = (collapsed: string[] = []) => rows(tree, new Set(collapsed)).map((r) => `${'  '.repeat(r.depth)}${r.node.name}`)

test('folders come first, then files, in natural order; greyed files sit in their folders', () => {
  expect(flat()).toEqual([
    'modules', '  01-a', '    lab', '      main.tf', '    reading', '      a.md', '      b.md', '    module.yaml', '  02-b', '    module.yaml', '  10-c', '    lab', '      setup.sh',
    'training.yaml',
  ])
  const tf = rows(tree, new Set()).find((r) => r.node.path === 'modules/01-a/lab/main.tf')!
  expect(tf.node.kind === 'file' && tf.node.reason).toContain('only .md')
})

test('a collapsed folder hides everything under it', () => {
  expect(flat(['modules/01-a', 'modules/10-c'])).toEqual(['modules', '  01-a', '  02-b', '    module.yaml', '  10-c', 'training.yaml'])
  expect(flat(['modules'])).toEqual(['modules', 'training.yaml'])
})

test('ancestors and folder of a path', () => {
  expect(ancestors('modules/01-a/reading/a.md')).toEqual(['modules', 'modules/01-a', 'modules/01-a/reading'])
  expect(ancestors('training.yaml')).toEqual([])
  expect(dirOf('modules/01-a/reading/a.md')).toBe('modules/01-a/reading/')
  expect(dirOf('training.yaml')).toBe('')
})

test('icons follow the extension', () => {
  expect(['a.md', 'a.yaml', 'a.yml', 'a.sh', 'a.tf', 'Makefile'].map(iconOf)).toEqual(['md', 'yaml', 'yaml', 'sh', 'file', 'file'])
})

test('the menu: training.yaml and greyed files only offer New file, read-only offers nothing', () => {
  const at = (p: string) => rows(tree, new Set()).find((r) => r.node.path === p)!.node
  expect(menuFor(at('modules/01-a/reading/a.md'), false)).toEqual(['new', 'rename', 'delete'])
  expect(menuFor(at('training.yaml'), false)).toEqual(['new'])
  expect(menuFor(at('modules/01-a/lab/main.tf'), false)).toEqual(['new'])
  expect(menuFor(at('modules/01-a'), false)).toEqual(['new'])
  expect(menuFor(at('modules/01-a/reading/a.md'), true)).toEqual([])
})

test('arrow keys walk the visible rows, open and close folders, and Enter opens a file', () => {
  const c = new Set(['modules/02-b'])
  const r = rows(tree, c)
  expect(treeKey(r, 'modules', 'ArrowDown', c)).toEqual({ focus: 'modules/01-a' })
  expect(treeKey(r, 'modules', 'ArrowUp', c)).toEqual({})
  expect(treeKey(r, 'training.yaml', 'ArrowDown', c)).toEqual({})
  expect(treeKey(r, 'modules/02-b', 'ArrowRight', c)).toEqual({ expand: 'modules/02-b' })
  expect(treeKey(r, 'modules/01-a', 'ArrowRight', c)).toEqual({ focus: 'modules/01-a/lab' })
  expect(treeKey(r, 'modules/01-a', 'ArrowLeft', c)).toEqual({ collapse: 'modules/01-a' })
  expect(treeKey(r, 'modules/01-a/module.yaml', 'ArrowLeft', c)).toEqual({ focus: 'modules/01-a' })
  expect(treeKey(r, 'modules/02-b', 'ArrowLeft', c)).toEqual({ focus: 'modules' })
  expect(treeKey(r, 'modules/02-b', 'Enter', c)).toEqual({ expand: 'modules/02-b' })
  expect(treeKey(r, 'modules/01-a', 'Enter', c)).toEqual({ collapse: 'modules/01-a' })
  expect(treeKey(r, 'modules/01-a/module.yaml', 'Enter', c)).toEqual({ open: 'modules/01-a/module.yaml' })
  expect(treeKey(r, 'modules/01-a/lab/main.tf', 'Enter', c)).toEqual({}) // greyed: nothing to open
  expect(treeKey(r, 'modules/01-a', 'Home', c)).toEqual({ focus: 'modules' })
  expect(treeKey(r, 'modules/01-a', 'End', c)).toEqual({ focus: 'training.yaml' })
  expect(treeKey(r, 'modules/01-a', 'x', c)).toBeUndefined()
})
