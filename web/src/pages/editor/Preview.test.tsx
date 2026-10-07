import { expect, test } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { Preview } from './Preview'

// The prompt and task text render through the lazy Markdown (a Suspense fallback in static markup), so these assert
// the non-lazy parts: ids, options, answer marks, costs.
test('a quiz preview marks the right answers (only editors ever see it)', () => {
  const html = renderToStaticMarkup(<Preview path="modules/m1/quiz.yaml" read={() => undefined}
    text={'questions:\n  - {id: s, type: single, prompt: Hot?, options: ["no", "yes"], answer: 1}\n'} />)
  expect(html).toContain('s · single')
  expect(html).toMatch(/yes[^<]*<\/span>\s*<span[^>]*>\s*✓ right answer/)
  expect(html).not.toMatch(/no[^<]*<\/span>\s*<span[^>]*>\s*✓ right answer/)
})

test('a lab preview shows tasks, terminals and hint costs; nothing runs', () => {
  const html = renderToStaticMarkup(<Preview path="modules/m1/lab/lab.yaml" read={(p) => (p === 'modules/m1/lab/tasks/1.md' ? '# Fix it' : undefined)}
    text={'id: l\nruntime: local\nterminals: [{name: shell, service: shell}]\ntasks:\n  - {id: t1, instructions: tasks/1.md, check: {script: c.sh, run_in: shell}, hints: [{text: Try, cost: 0.5}]}\n'} />)
  expect(html).toContain('shell')
  expect(html).toContain('t1')
  expect(html).toContain('costs 0.5')
})

test('a broken file shows the parse error instead of a preview', () => {
  expect(renderToStaticMarkup(<Preview path="modules/m1/quiz.yaml" read={() => undefined} text="questions: [" />)).toContain('role="alert"')
})
