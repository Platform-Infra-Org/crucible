import { expect, test } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { DiffView } from './DiffView'
import { editProblem } from '../lib/editLimits'
import { conflictNotice } from '../lib/conflictNotice'
import { byteLen, changedFiles, headVersions } from '../lib/editDraft'

test('diff lines are marked added, removed or context', () => {
  const html = renderToStaticMarkup(<DiffView diff={'diff --git a/x.md b/x.md\n@@ -1,2 +1,2 @@\n # Intro\n-Hello.\n+Hello, smith.\n'} />)
  expect(html).toContain('data-testid="diff"')
  expect(html).toContain('class="diff-del"')
  expect(html).toContain('class="diff-add"')
  expect(html).toContain('class="diff-hunk"')
  expect(html).toContain('+Hello, smith.')
})

test('diff text is escaped, never parsed as HTML', () => {
  const html = renderToStaticMarkup(<DiffView diff={'+<img src=x onerror=alert(1)><script>x</script>'} />)
  expect(html).not.toContain('<img')
  expect(html).not.toContain('<script')
  expect(html).toContain('&lt;img')
})

test('carriage returns, tabs, trailing spaces and control characters are visible', () => {
  const html = renderToStaticMarkup(<DiffView diff={'+a\tb \r\n+x\u0007y‮z​  '} />)
  expect(html).toContain('␍')
  expect(html).toContain('⇥')
  expect(html).toContain('‹U+0007›')
  expect(html).toContain('‹U+202E›')
  expect(html).toContain('‹U+200B›')
  expect(html).toContain('class="diff-ws"')
  expect(html).not.toContain('\r')
  expect(html).not.toContain('\u0007')
})

test('every invisible code point is marked, astral ones included', () => {
  for (const cp of [0x61c, 0x180e, 0xfff9, 0x3164, 0x34f, 0xfe0f, 0xe0041, 0xe0100]) {
    const u = cp.toString(16).toUpperCase().padStart(4, '0')
    expect(renderToStaticMarkup(<DiffView diff={`+a${String.fromCodePoint(cp)}b`} />), u).toContain(`‹U+${u}›`)
  }
  expect(renderToStaticMarkup(<DiffView diff={'+plain 😀 ünï'} />)).not.toContain('‹')
})

test('CRLF files keep their line endings, unchanged files are not submitted', () => {
  const orig = { 'a.md': 'x\r\ny\r\n', 'b.md': 'k\n' }
  expect(changedFiles(orig, { 'a.md': 'x\r\ny\r\n', 'b.md': 'k\n' })).toEqual({})
  expect(changedFiles(orig, { 'a.md': 'x\nz\n', 'b.md': 'k\nm\n' })).toEqual({ 'a.md': 'x\r\nz\r\n', 'b.md': 'k\nm\n' })
  expect(byteLen('é')).toBe(2)
})

test('redo seeds paths with the head text, not the stale edit', async () => {
  const head = await headVersions(['a.md', 'new.md'], async (p) => { if (p === 'new.md') throw new Error('404'); return 'HEAD ' + p })
  expect(head).toEqual({ 'a.md': 'HEAD a.md' })
  expect(changedFiles(head, head)).toEqual({})
})

test('client limits mirror the server', () => {
  expect(editProblem({ 'training.yaml': 'a' })).toBeUndefined()
  expect(editProblem({ 'modules/m1/a.md': 'a' })).toBeUndefined()
  expect(editProblem({})).toMatch(/1 to 20/)
  expect(editProblem(Object.fromEntries(Array.from({ length: 21 }, (_, i) => [`modules/m/${i}.md`, 'a'])))).toMatch(/1 to 20/)
  expect(editProblem({ 'README.md': 'a' })).toMatch(/modules/)
  expect(editProblem({ 'modules/m/a.png': 'a' })).toMatch(/only \.md/)
  expect(editProblem({ 'modules/m/a.md': 'a'.repeat(256 * 1024 + 1) })).toMatch(/256 KiB/)
})

test('409s are told apart', () => {
  expect(conflictNotice('this edit changed since it was reviewed; review it again')).toMatch(/^Edit moved/)
  expect(conflictNotice('this edit no longer applies to the current content; the author can redo it')).toMatch(/^No longer applies — redo it/)
})
