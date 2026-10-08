import type { JourneyRow } from '../types'

export type By = 'person' | 'training'
export type Group = { key: string; rows: JourneyRow[] }

// group gathers the rows under each person or each training, people by name and trainings by title.
export function group(rows: JourneyRow[], by: By): Group[] {
  const groups = new Map<string, JourneyRow[]>()
  for (const r of rows) {
    const k = by === 'person' ? r.email : r.training
    groups.set(k, [...(groups.get(k) ?? []), r])
  }
  const label = (g: Group) => (by === 'person' ? g.rows[0].name || g.key : g.rows[0].title)
  const inner = (r: JourneyRow) => (by === 'person' ? r.title : r.name || r.email)
  return [...groups].map(([key, rs]) => ({ key, rows: rs.sort((a, b) => inner(a).localeCompare(inner(b))) }))
    .sort((a, b) => label(a).localeCompare(label(b)))
}

export const average = (rows: JourneyRow[]) => (rows.length ? Math.round(rows.reduce((n, r) => n + r.percent, 0) / rows.length) : 0)

export type Filter = { person: string; training: string; look: boolean }

// pick keeps the rows that match every filter that is set ('' and false match everything).
export const pick = (rows: JourneyRow[], f: Filter) =>
  rows.filter((r) => (!f.person || r.email === f.person) && (!f.training || r.training === f.training) && (!f.look || r.flags.length > 0))

// options lists each person and each training once, by name and by title, for the filter pickers.
export function options(rows: JourneyRow[]) {
  const people = new Map(rows.map((r) => [r.email, r.name || r.email]))
  const trainings = new Map(rows.map((r) => [r.training, r.title]))
  const sorted = (m: Map<string, string>) => [...m].map(([value, label]) => ({ value, label })).sort((a, b) => a.label.localeCompare(b.label))
  return { people: sorted(people), trainings: sorted(trainings) }
}
