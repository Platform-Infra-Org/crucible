import { expect, test } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { BlocksPanel } from './BlocksPanel'
import type { BlockInfo } from '../../types'

const blocks: BlockInfo[] = [
  { id: 'reading', group: 'Reading', title: 'Reading', summary: 'A Markdown page.', file_kind: 'reading', example: '# Tongs',
    fields: [{ name: 'module', type: 'module', required: true, description: 'Which module?' }, { name: 'title', type: 'string', required: true, description: 'Title' }] },
  { id: 'template.lab.aws', group: 'AWS', title: 'AWS lab with an S3 bucket', summary: 'S3.', file_kind: 'lab', example: 'runtime: aws', git_only: true, fields: [] },
]
const render = (preselect?: string) => renderToStaticMarkup(<BlocksPanel groups={['Reading', 'AWS']} blocks={blocks} paths={['modules/01-a/module.yaml']}
  read={() => undefined} need={() => {}} preselect={preselect} onInsert={async () => {}} />)

test('blocks are listed by group with their summary', () => {
  const html = render()
  expect(html).toContain('<h3>Reading</h3>')
  expect(html).toContain('A Markdown page.')
})

test('a form is generated from the fields: required markers, a module picker, help text', () => {
  const html = render('reading')
  expect(html).toContain('<select')
  expect(html).toContain('01-a')
  expect(html).toContain('aria-required="true"')
  expect(html).toContain('Which module?')
  expect(html).toContain('Add to the draft')
})

test('git-only blocks show their example and say where to add them', () => {
  const html = render('template.lab.aws')
  expect(html).toContain('runtime: aws')
  expect(html).toContain('Add this in git')
  expect(html).not.toContain('Add to the draft')
})
