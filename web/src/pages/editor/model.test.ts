import { expect, test } from 'vitest'
import { applyInsert, changeList, fileText, currentPaths, deletePath, emptyOps, fromOps, languageOf, matchFiles, monacoTheme, monacoThemeData, toHex, origin, putText, renamePath, toOps } from './model'

const base = ['training.yaml', 'modules/m1/module.yaml', 'modules/m1/reading/a.md', 'modules/m1/reading/b.md']

test('ops round-trip in the order the server applies them', () => {
  const ops = [
    { op: 'put' as const, path: 'modules/m1/reading/c.md', content: 'C' },
    { op: 'delete' as const, path: 'modules/m1/reading/b.md' },
    { op: 'rename' as const, from: 'modules/m1/reading/a.md', to: 'modules/m1/reading/z.md' },
  ]
  expect(toOps(fromOps(ops)).map((o) => o.op)).toEqual(['rename', 'delete', 'put'])
  expect(currentPaths(base, fromOps(ops))).toEqual(['modules/m1/module.yaml', 'modules/m1/reading/c.md', 'modules/m1/reading/z.md', 'training.yaml'])
})

test('a file edited back to its original leaves the draft', () => {
  let d = putText(emptyOps(), 'modules/m1/reading/a.md', 'A2', 'A')
  expect(d.puts).toEqual({ 'modules/m1/reading/a.md': 'A2' })
  d = putText(d, 'modules/m1/reading/a.md', 'A', 'A')
  expect(toOps(d)).toEqual([])
})

test('rename keeps unsaved text, chains collapse, renaming back undoes it', () => {
  let d = putText(emptyOps(), 'modules/m1/reading/a.md', 'A2', 'A')
  d = renamePath(base, d, 'modules/m1/reading/a.md', 'modules/m1/reading/x.md')
  d = renamePath(base, d, 'modules/m1/reading/x.md', 'modules/m2/reading/y.md')
  expect(d.renames).toEqual({ 'modules/m2/reading/y.md': 'modules/m1/reading/a.md' })
  expect(d.puts).toEqual({ 'modules/m2/reading/y.md': 'A2' })
  expect(origin(base, d, 'modules/m2/reading/y.md')).toBe('modules/m1/reading/a.md')
  expect(origin(base, d, 'modules/m1/reading/a.md')).toBeUndefined()
  d = renamePath(base, d, 'modules/m2/reading/y.md', 'modules/m1/reading/a.md')
  expect(d.renames).toEqual({})
  expect(() => renamePath(base, d, 'modules/m1/reading/a.md', 'modules/m1/reading/b.md')).toThrow(/already exists/)
})

test('renaming a new file moves its put; deleting it forgets it', () => {
  let d = putText(emptyOps(), 'modules/m1/reading/n.md', 'N', undefined)
  d = renamePath(base, d, 'modules/m1/reading/n.md', 'modules/m1/reading/m.md')
  expect(toOps(d)).toEqual([{ op: 'put', path: 'modules/m1/reading/m.md', content: 'N' }])
  expect(toOps(deletePath(base, d, 'modules/m1/reading/m.md'))).toEqual([])
})

test('deleting a renamed file deletes its original; a deleted name is not reused by a rename', () => {
  let d = renamePath(base, emptyOps(), 'modules/m1/reading/a.md', 'modules/m1/reading/x.md')
  d = deletePath(base, d, 'modules/m1/reading/x.md')
  expect(toOps(d)).toEqual([{ op: 'delete', path: 'modules/m1/reading/a.md' }])
  expect(() => renamePath(base, d, 'modules/m1/reading/b.md', 'modules/m1/reading/a.md')).toThrow(/deleted/)
})

test('changes list what a reviewer will see', () => {
  let d = renamePath(base, emptyOps(), 'modules/m1/reading/a.md', 'modules/m1/reading/x.md')
  d = deletePath(base, d, 'modules/m1/reading/b.md')
  d = putText(d, 'modules/m1/module.yaml', 'title: M\n', 'title: M1\n')
  d = putText(d, 'modules/m1/reading/new.md', '# New\n', undefined)
  expect(changeList(base, d)).toEqual([
    { kind: 'changed', path: 'modules/m1/module.yaml' },
    { kind: 'deleted', path: 'modules/m1/reading/b.md' },
    { kind: 'added', path: 'modules/m1/reading/new.md' },
    { kind: 'renamed', path: 'modules/m1/reading/x.md', from: 'modules/m1/reading/a.md' },
  ])
})

test('go to file matches in order, best name first', () => {
  expect(matchFiles(base, 'rdb')).toEqual(['modules/m1/reading/b.md'])
  expect(matchFiles(base, 'module')[0]).toBe('modules/m1/module.yaml')
  expect(matchFiles(base, '')).toEqual(base)
})

test('languages and themes', () => {
  expect([languageOf('a.md'), languageOf('a.sh'), languageOf('a.yml'), languageOf('a.yaml')]).toEqual(['markdown', 'shell', 'yaml', 'yaml'])
  expect([monacoTheme('forge'), monacoTheme('quench'), monacoTheme('contrast'), monacoTheme('anvil'), monacoTheme(undefined)])
    .toEqual(['crucible-forge', 'crucible-quench', 'crucible-contrast', 'crucible-anvil', 'crucible-forge'])
})

test('css colours become the hex Monaco wants', () => {
  expect([toHex(' #14110F'), toHex('#000'), toHex('#fff8'), toHex('rgb(255, 122, 26)'), toHex('rgba(255, 122, 26, 0.5)'), toHex('rgb(0 0 0 / 25%)')])
    .toEqual(['#14110f', '#000000', '#ffffff88', '#ff7a1a', '#ff7a1a80', '#00000040'])
  expect([toHex(''), toHex('none'), toHex('#12'), toHex('var(--x)')]).toEqual([undefined, undefined, undefined, undefined])
})

test('a Monaco theme takes its colours from the app theme tokens', () => {
  const forge: Record<string, string> = { '--bg': '#14110f', '--surface': '#1f1a17', '--surface-2': '#2a231f', '--text': '#f3e9df', '--muted': '#b5a596',
    '--accent': '#ff7a1a', '--accent-2': '#ffc24b', '--danger': '#ff5a4d', '--ok': '#7bd88f', '--border': '#3a302a', '--diff-add': '#7bd88f', '--diff-del': '#ff5a4d' }
  const t = monacoThemeData('forge', (v) => forge[v] ?? '')
  expect(t.base).toBe('vs-dark')
  expect(t.colors['editor.background']).toBe('#14110f')
  expect(t.colors['editor.foreground']).toBe('#f3e9df')
  expect(t.colors['editorCursor.foreground']).toBe('#ff7a1a')
  expect(t.colors['editorLineNumber.activeForeground']).toBe('#ff7a1a')
  expect(t.colors['editor.selectionBackground']).toBe('#ff7a1a40')
  expect(t.colors['editorHoverWidget.background']).toBe('#1f1a17')
  expect(t.colors['diffEditor.insertedTextBackground']).toBe('#7bd88f33')
  expect(t.rules).toContainEqual({ token: 'type', foreground: 'ffc24b' })
  expect(t.rules).toContainEqual({ token: 'comment', foreground: 'b5a596', fontStyle: 'italic' })
  expect(monacoThemeData('anvil', (v) => forge[v] ?? '').base).toBe('vs')
  expect(monacoThemeData('contrast', (v) => forge[v] ?? '').base).toBe('hc-black')
  // No tokens (not loaded yet): Monaco's own colours stay, nothing invalid reaches defineTheme.
  const bare = monacoThemeData('quench', () => '')
  expect([bare.base, Object.keys(bare.colors).length, bare.rules.length]).toEqual(['vs-dark', 0, 0])
})

test('deleting a file and creating one at the same path is one put, never delete+put of a path', () => {
  let d = deletePath(base, emptyOps(), 'modules/m1/reading/a.md')
  d = putText(d, 'modules/m1/reading/a.md', '', undefined)
  expect(toOps(d)).toEqual([{ op: 'put', path: 'modules/m1/reading/a.md', content: '' }])
  expect(origin(base, d, 'modules/m1/reading/a.md')).toBe('modules/m1/reading/a.md')
  expect(changeList(base, d)).toEqual([{ kind: 'changed', path: 'modules/m1/reading/a.md' }])
})

test('reopening an edit keeps its renames and deletes', () => {
  const ops = [
    { op: 'rename' as const, from: 'modules/m1/reading/a.md', to: 'modules/m1/reading/z.md' },
    { op: 'delete' as const, path: 'modules/m1/reading/b.md' },
  ]
  expect(toOps(fromOps(ops))).toEqual(ops)
})

test('a file whose base text is not loaded has no text yet, never ""', () => {
  const d = putText(emptyOps(), 'modules/m1/reading/n.md', '', undefined)
  expect(fileText(base, d, {}, 'modules/m1/reading/a.md')).toBeUndefined() // after a reload cleared the base texts
  expect(fileText(base, d, { 'modules/m1/reading/a.md': 'A' }, 'modules/m1/reading/a.md')).toBe('A')
  expect(fileText(base, d, {}, 'modules/m1/reading/n.md')).toBe('') // a new, empty file
  expect(fileText(base, deletePath(base, d, 'modules/m1/reading/b.md'), {}, 'modules/m1/reading/b.md')).toBeUndefined()
})

test('an insert applies to the draft as it is now, unless a file it writes changed since the request went out', () => {
  const sent = putText(emptyOps(), 'modules/m1/quiz.yaml', 'Q1', undefined)
  const puts = [{ path: 'modules/m1/quiz.yaml', content: 'Q1+q2' }, { path: 'modules/m1/module.yaml', content: 'M+quiz' }]
  const elsewhere = putText(sent, 'modules/m1/reading/a.md', 'A2', 'A') // typing in a file the block doesn't write is kept
  expect(applyInsert(base, sent, elsewhere, puts, { 'modules/m1/module.yaml': 'M' })).toEqual({
    next: { ...elsewhere, puts: { ...elsewhere.puts, 'modules/m1/quiz.yaml': 'Q1+q2', 'modules/m1/module.yaml': 'M+quiz' } },
  })
  // typed in, deleted or renamed meanwhile: refused, so the author's work stays as it is
  expect(applyInsert(base, sent, putText(sent, 'modules/m1/quiz.yaml', 'Q1 typed', undefined), puts, {})).toEqual({ changed: 'modules/m1/quiz.yaml' })
  expect(applyInsert(base, sent, deletePath(base, sent, 'modules/m1/module.yaml'), puts, {})).toEqual({ changed: 'modules/m1/module.yaml' })
  expect(applyInsert(base, sent, renamePath(base, sent, 'modules/m1/module.yaml', 'modules/m1/mod.yaml'), puts, {})).toEqual({ changed: 'modules/m1/module.yaml' })
})
