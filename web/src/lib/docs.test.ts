import { expect, test } from 'vitest'
import { docSections, helpFor, landing, rolesOf, searchDocs, type DocPage } from './docs'
import type { Me } from '../types'

const p = (slug: string, roles: string[], covers: string[] = [], order = 0, headings: string[] = []): DocPage =>
  ({ slug, section: slug.split('/')[0], title: slug.split('/')[1], roles, covers, order, headings })
const pages = [
  p('getting-started/the-hearth', ['everyone'], ['route:/', 'route:/trainings'], 20),
  p('getting-started/signing-in', ['everyone'], [], 10),
  p('trainees/laptop-labs', ['trainee'], ['route:/p/:team/:training/m/:module/lab'], 30, ['Leaving the terminal']),
  p('authors/editing-content', ['author'], ['route:/edits/new'], 20),
  p('leaders/approvals', ['approver', 'leader'], ['route:/approvals'], 30),
  p('admins/forge-status', ['admin'], ['route:/admin'], 10),
]
const me = (over: Partial<Me>): Me => ({ user: { id: 1, email: 'x@y', name: 'X', theme: '', calm_motion: false }, is_admin: false, default_theme: 'forge',
  teams: [], can_approve: false, can_score: false, can_view_spend: false, is_mentor: false, can_edit_content: false, config_in_db: false, ...over })

test('roles come from /api/me flags', () => {
  expect(rolesOf(me({}))).toEqual(['everyone', 'trainee'])
  expect(rolesOf(me({ can_edit_content: true, can_approve: true, is_admin: true }))).toEqual(['everyone', 'trainee', 'author', 'approver', 'leader', 'admin'])
})

test('my sections come first, ordered by my highest role; All shows the rest', () => {
  const admin = rolesOf(me({ is_admin: true }))
  expect(docSections(pages, admin, false).map((s) => s.id)).toEqual(['admins', 'trainees', 'getting-started'])
  expect(docSections(pages, admin, true).map((s) => s.id)).toEqual(['admins', 'trainees', 'getting-started', 'leaders', 'authors'])
  expect(docSections(pages, rolesOf(me({})), false)[0].pages.map((x) => x.slug)).toEqual(['trainees/laptop-labs'])
  expect(docSections(pages, rolesOf(me({})), false)[1].pages.map((x) => x.slug)).toEqual(['getting-started/signing-in', 'getting-started/the-hearth'])
})

test('Docs opens on the first page of my highest section', () => {
  expect(landing(pages, rolesOf(me({ is_admin: true })))).toBe('admins/forge-status')
  expect(landing(pages, rolesOf(me({ can_approve: true })))).toBe('leaders/approvals')
  expect(landing(pages, rolesOf(me({})))).toBe('trainees/laptop-labs')
})

test('? links match route patterns exactly', () => {
  expect(helpFor(pages, '/')?.slug).toBe('getting-started/the-hearth')
  expect(helpFor(pages, '/p/forge/forge-101/m/02-first-lab/lab')?.slug).toBe('trainees/laptop-labs')
  expect(helpFor(pages, '/edits/new')?.slug).toBe('authors/editing-content')
  expect(helpFor(pages, '/nowhere')).toBeUndefined()
})

test('search matches titles and headings, case-insensitively', () => {
  expect(searchDocs(pages, 'TERMINAL').map((x) => x.slug)).toEqual(['trainees/laptop-labs'])
  expect(searchDocs(pages, 'forge-st').map((x) => x.slug)).toEqual(['admins/forge-status'])
  expect(searchDocs(pages, '')).toHaveLength(pages.length)
})
