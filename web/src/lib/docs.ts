import { matchPath } from 'react-router'
import type { Me } from '../types'

export type DocPage = { slug: string; section: string; title: string; roles: string[]; covers: string[]; order: number; headings: string[] }
export type DocSection = { id: string; title: string; mine: boolean; pages: DocPage[] }

const SECTIONS: { id: string; title: string; roles: string[] }[] = [
  { id: 'admins', title: 'Admins', roles: ['admin'] },
  { id: 'leaders', title: 'Leaders and approvers', roles: ['leader', 'approver'] },
  { id: 'scorers', title: 'Scorers', roles: ['scorer'] },
  { id: 'authors', title: 'Authors', roles: ['author'] },
  { id: 'trainees', title: 'Trainees', roles: ['trainee'] },
  { id: 'getting-started', title: 'Getting started', roles: ['everyone'] },
] // highest role first: Docs opens on the first section that is mine

export function rolesOf(me: Me): string[] {
  const r = ['everyone', 'trainee']
  if (me.can_edit_content) r.push('author')
  if (me.can_score) r.push('scorer')
  if (me.can_approve) r.push('approver', 'leader')
  if (me.is_admin) r.push('admin')
  return r
}

export function docSections(pages: DocPage[], roles: string[], all: boolean): DocSection[] {
  const out = SECTIONS.map((s) => ({
    id: s.id, title: s.title, mine: s.roles.some((r) => roles.includes(r)),
    pages: pages.filter((p) => p.section === s.id).sort((a, b) => a.order - b.order || a.slug.localeCompare(b.slug)),
  })).filter((s) => s.pages.length > 0 && (all || s.mine))
  return [...out.filter((s) => s.mine), ...out.filter((s) => !s.mine)]
}

export const landing = (pages: DocPage[], roles: string[]) => docSections(pages, roles, false)[0]?.pages[0]?.slug

export function helpFor(pages: DocPage[], pathname: string): DocPage | undefined {
  for (const s of docSections(pages, ['everyone'], true)) {
    for (const p of s.pages) {
      if (p.covers.some((c) => c.startsWith('route:') && matchPath(c.slice('route:'.length), pathname))) return p
    }
  }
}

export function searchDocs(pages: DocPage[], q: string): DocPage[] {
  const s = q.trim().toLowerCase()
  return s ? pages.filter((p) => [p.title, ...p.headings].some((t) => t.toLowerCase().includes(s))) : pages
}
