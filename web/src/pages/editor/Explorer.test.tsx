import { expect, test } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { Explorer } from './Explorer'

const noop = () => {}
const render = (readOnly: boolean) => renderToStaticMarkup(
  <Explorer paths={['training.yaml', 'modules/m1/reading/a.md']} greyed={[{ path: 'modules/m1/lab/terraform/main.tf', reason: 'only .md, .yaml, .yml and .sh files can be edited here' }]}
    changed={new Set(['modules/m1/reading/a.md'])} active="modules/m1/reading/a.md" readOnly={readOnly}
    onOpen={noop} onNew={noop} onRename={noop} onDelete={noop} onNewModule={noop} />,
)
const html = render(false)

test('files show as a tree of folders, folders open', () => {
  expect(html).toContain('role="tree"')
  expect(html.match(/role="treeitem"/g)).toHaveLength(8) // modules, m1, lab, terraform, main.tf, reading, a.md, training.yaml
  expect(html.match(/aria-expanded="true"/g)).toHaveLength(5)
})

test('files that cannot be edited are greyed with the reason', () => {
  expect(html).toContain('modules/m1/lab/terraform/main.tf (can&#x27;t be edited here: only .md, .yaml, .yml and .sh files can be edited here)')
})

test('changed files are marked, the open file is current', () => {
  expect(html).toContain('aria-current="true"')
  expect(html).toContain('(changed)')
})

test('the header offers New file, New module and Collapse all; read-only keeps only Collapse all', () => {
  for (const name of ['New file', 'New module', 'Collapse all']) expect(html).toContain(`aria-label="${name}"`)
  const ro = render(true)
  expect(ro).not.toContain('aria-label="New file"')
  expect(ro).not.toContain('aria-label="New module"')
  expect(ro).toContain('aria-label="Collapse all"')
})
