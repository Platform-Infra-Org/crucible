import { expect, test } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { Explorer } from './Explorer'

const noop = () => {}
const html = renderToStaticMarkup(
  <Explorer paths={['training.yaml', 'modules/m1/reading/a.md']} greyed={[{ path: 'modules/m1/lab/terraform/main.tf', reason: 'only .md, .yaml, .yml and .sh files can be edited here' }]}
    changed={new Set(['modules/m1/reading/a.md'])} active="modules/m1/reading/a.md" readOnly={false}
    onOpen={noop} onNew={noop} onRename={noop} onDelete={noop} />,
)

test('training.yaml cannot be renamed or deleted; other files can', () => {
  expect(html).not.toContain('aria-label="Rename training.yaml"')
  expect(html).not.toContain('aria-label="Delete training.yaml"')
  expect(html).toContain('aria-label="Rename modules/m1/reading/a.md"')
  expect(html).toContain('aria-label="Delete modules/m1/reading/a.md"')
})

test('files that cannot be edited are greyed with the reason', () => {
  expect(html).toContain('modules/m1/lab/terraform/main.tf (can&#x27;t be edited here: only .md, .yaml, .yml and .sh files can be edited here)')
})

test('changed files are marked, the open file is current', () => {
  expect(html).toContain('aria-current="true"')
  expect(html).toContain('(changed)')
})
