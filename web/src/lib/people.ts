import type { Member, Tag } from '../types'
import { TAGS } from './teamRequests'

// How the team page shows its people: a search, one tag or all, the order, and whether to group them by tag.
export type PeopleQuery = { q: string; tag: Tag | ''; sort: 'tag' | 'name'; group: boolean }

const rank = (t: Tag) => TAGS.indexOf(t)
const label = (m: Member) => (m.name || m.email).toLowerCase()

// peopleView filters and orders members, and groups them by tag (strongest first) when asked; a tag with nobody left
// after the filter is dropped. Without grouping there is one group with tag ''.
export function peopleView(members: Member[], q: PeopleQuery): { tag: Tag | ''; people: Member[] }[] {
  const needle = q.q.trim().toLowerCase()
  const rows = members
    .filter((m) => !q.tag || m.tag === q.tag)
    .filter((m) => !needle || m.email.includes(needle) || m.name.toLowerCase().includes(needle))
    .sort((a, b) => (q.sort === 'tag' ? rank(a.tag) - rank(b.tag) : 0) || label(a).localeCompare(label(b)))
  if (!q.group) return [{ tag: '', people: rows }]
  return TAGS.map((tag) => ({ tag, people: rows.filter((m) => m.tag === tag) })).filter((g) => g.people.length > 0)
}

// readQuery and writeQuery keep the view in the URL (?q=…&tag=…&sort=name&by=tag), so it can be shared or reloaded.
export function readQuery(p: URLSearchParams): PeopleQuery {
  const tag = p.get('tag') ?? ''
  return { q: p.get('q') ?? '', tag: (TAGS as string[]).includes(tag) ? (tag as Tag) : '', sort: p.get('sort') === 'name' ? 'name' : 'tag', group: p.get('by') === 'tag' }
}

export function writeQuery(q: PeopleQuery): Record<string, string> {
  const out: Record<string, string> = {}
  if (q.q) out.q = q.q
  if (q.tag) out.tag = q.tag
  if (q.sort === 'name') out.sort = 'name'
  if (q.group) out.by = 'tag'
  return out
}
