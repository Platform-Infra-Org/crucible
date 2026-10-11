import { describe, expect, test } from 'vitest'
import type { Member } from '../types'
import { peopleView, readQuery, writeQuery } from './people'

const team: Member[] = [
  { email: 't2@x', name: 'Bea', tag: 'trainee' }, { email: 'l@x', name: 'Zed', tag: 'leader' },
  { email: 's@x', name: '', tag: 'senior' }, { email: 't1@x', name: 'Ann', tag: 'trainee' },
]
const q = { q: '', tag: '' as const, sort: 'tag' as const, group: false }
const emails = (v: ReturnType<typeof peopleView>) => v.map((g) => `${g.tag}:${g.people.map((m) => m.email).join(',')}`)

describe('the people list', () => {
  test('by tag, strongest first, then by name (or email when there is none)', () => {
    expect(emails(peopleView(team, q))).toEqual([':l@x,s@x,t1@x,t2@x'])
    expect(emails(peopleView(team, { ...q, sort: 'name' }))).toEqual([':t1@x,t2@x,s@x,l@x'])
  })
  test('finds by name or email, and filters by tag', () => {
    expect(emails(peopleView(team, { ...q, q: 'BE' }))).toEqual([':t2@x'])
    expect(emails(peopleView(team, { ...q, q: 's@' }))).toEqual([':s@x'])
    expect(emails(peopleView(team, { ...q, tag: 'trainee' }))).toEqual([':t1@x,t2@x'])
  })
  test('grouped by tag, empty tags left out', () => {
    expect(emails(peopleView(team, { ...q, group: true }))).toEqual(['leader:l@x', 'senior:s@x', 'trainee:t1@x,t2@x'])
    expect(emails(peopleView(team, { ...q, group: true, q: 'nobody' }))).toEqual([])
  })
  test('the view lives in the URL, with defaults left out', () => {
    const v = { q: 'ann', tag: 'trainee' as const, sort: 'name' as const, group: true }
    expect(writeQuery(v)).toEqual({ q: 'ann', tag: 'trainee', sort: 'name', by: 'tag' })
    expect(readQuery(new URLSearchParams(writeQuery(v)))).toEqual(v)
    expect(writeQuery(q)).toEqual({})
    expect(readQuery(new URLSearchParams('tag=wizard&sort=odd'))).toEqual(q)
  })
})
