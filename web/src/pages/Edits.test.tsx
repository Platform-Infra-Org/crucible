import { expect, test, vi } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router'
import { EditList } from './Edits'

const e = { id: 1, training: 'f101', title: 'Typo', author: 'a@x', author_smith: { level: 2, title: 'Journeyman' }, status: 'pending' }
vi.mock('../useFetch', () => ({ useFetch: (p: string) => ({ data: p === '/api/edits' ? [e] : [], error: undefined, reload: () => {} }) }))
vi.mock('../me', () => ({ useMe: () => ({ me: { can_edit_content: false } }) }))

test('the edits list shows the author with their smith rank', () => {
  const h = renderToStaticMarkup(<MemoryRouter><EditList /></MemoryRouter>)
  expect(h).toContain('a@x')
  expect(h).toContain('smith-mark l2')
})
