import { expect, test } from 'vitest'
import { bounds, DEFAULT_LAYOUT, fit, MIN, parseLayout, resize } from './layout'

const l = { side: 300, preview: 500, previewOpen: true }

test('stored widths show as they are when they fit', () => {
  expect(fit(l, 2000, true)).toEqual({ side: 300, preview: 500 })
  expect(fit(l, 2000, false)).toEqual({ side: 300, preview: 0 })
})

test('a narrow window shrinks the preview first, then the side panel, never under their minimums', () => {
  expect(fit(l, 1000, true)).toEqual({ side: 300, preview: 1000 - 300 - MIN.editor })
  expect(fit(l, 800, true)).toEqual({ side: 800 - MIN.editor - MIN.preview, preview: MIN.preview })
  expect(fit(l, 100, true)).toEqual({ side: MIN.side, preview: MIN.preview })
  expect(fit(l, 600, false)).toEqual({ side: 600 - MIN.editor, preview: 0 })
})

test('bounds leave the editor its minimum', () => {
  expect(bounds(l, 2000, true, 'side')).toEqual({ min: MIN.side, max: 2000 - MIN.editor - 500 })
  expect(bounds(l, 2000, true, 'preview')).toEqual({ min: MIN.preview, max: 2000 - MIN.editor - 300 })
  expect(bounds(l, 2000, false, 'side')).toEqual({ min: MIN.side, max: 2000 - MIN.editor })
  expect(bounds(l, 300, true, 'side').max).toBe(MIN.side) // never a max under the min
})

test('resizing clamps to the bounds', () => {
  expect(resize(l, 2000, true, 'side', 100).side).toBe(MIN.side)
  expect(resize(l, 2000, true, 'side', 5000).side).toBe(2000 - MIN.editor - 500)
  expect(resize(l, 2000, true, 'preview', 640.4)).toEqual({ ...l, preview: 640 })
})

test('a stored layout is read defensively', () => {
  expect(parseLayout(null)).toEqual(DEFAULT_LAYOUT)
  expect(parseLayout('not json')).toEqual(DEFAULT_LAYOUT)
  expect(parseLayout('{"side":"x","preview":null}')).toEqual(DEFAULT_LAYOUT)
  expect(parseLayout('{"side":10,"preview":9000,"previewOpen":false}')).toEqual({ side: MIN.side, preview: 9000, previewOpen: false })
  expect(parseLayout('{"side":333,"preview":444,"previewOpen":true}')).toEqual({ side: 333, preview: 444, previewOpen: true })
})
