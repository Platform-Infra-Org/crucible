import { expect, test } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { ProblemsPanel } from './ProblemsPanel'
import { ChangesPanel } from './ChangesPanel'
import { GoToFile } from './GoToFile'

const noop = () => {}

test('problems are buttons that name file and line; unknown files are plain text', () => {
  const html = renderToStaticMarkup(<ProblemsPanel problems={[
    { file: 'modules/m1/quiz.yaml', line: 2, msg: 'question 1 (q1): needs at least 2 options' },
    { file: 'modules/m1/reading/gone.md', line: 1, msg: 'file not found' },
  ]} known={(f) => f.endsWith('quiz.yaml')} onJump={noop} />)
  expect(html).toContain('<button')
  expect(html).toContain('modules/m1/quiz.yaml:2')
  expect(html).toContain('needs at least 2 options')
  expect(html.match(/<button/g)).toHaveLength(1)
})

test('no problems says so', () => {
  expect(renderToStaticMarkup(<ProblemsPanel problems={[]} known={() => true} onJump={noop} />)).toContain('No problems')
})

test('changes show their kind and the reviewers’ diff view', () => {
  const html = renderToStaticMarkup(<ChangesPanel changes={[{ kind: 'renamed', path: 'm/b.md', from: 'm/a.md' }, { kind: 'deleted', path: 'm/c.md' }]}
    diffOf={(c) => (c.kind === 'deleted' ? '-bye​' : undefined)} onOpen={noop} />)
  expect(html).toContain('Renamed')
  expect(html).toContain('m/a.md')
  expect(html).toContain('Deleted')
  expect(html).toContain('data-testid="diff"')
  expect(html).toContain('‹U+200B›') // hidden characters are marked, as reviewers see them
})

test('go to file lists matches as options', () => {
  const html = renderToStaticMarkup(<GoToFile paths={['training.yaml', 'modules/m1/quiz.yaml']} onPick={noop} onClose={noop} />)
  expect(html).toContain('role="dialog"')
  expect(html).toContain('role="listbox"')
  expect(html.match(/role="option"/g)).toHaveLength(2)
})
