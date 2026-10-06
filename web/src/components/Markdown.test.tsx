import { expect, test } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { Markdown } from './Markdown'

test('callouts, highlighted code and mermaid placeholders', () => {
  const md = '> [!WARNING]\n> Hot metal.\n\n```go\nfunc main() {}\n```\n\n```mermaid\ngraph TD; A-->B\n```\n'
  const html = renderToStaticMarkup(<Markdown text={md} />)
  expect(html).toContain('class="callout callout-warning"')
  expect(html).toContain('data-callout="WARNING"')
  expect(html).toContain('Hot metal.')
  expect(html).not.toContain('[!WARNING]')
  expect(html).toContain('hljs-keyword')
  expect(html).toContain('graph TD; A--&gt;B')
})

test('raw html stays inert', () => {
  const html = renderToStaticMarkup(<Markdown text={'<img src=x onerror=alert(1)><script>alert(1)</script>'} />)
  expect(html).not.toContain('<img')
  expect(html).not.toContain('<script')
})
