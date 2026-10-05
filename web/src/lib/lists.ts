// One email per line (or comma separated) → lowercased, de-duplicated list.
export function parseEmails(text: string): string[] {
  return [...new Set(text.split(/[\s,]+/).map((s) => s.trim().toLowerCase()).filter(Boolean))]
}

// "trainee = mentor" per line → { trainee: mentor }.
export function parseMentors(text: string): Record<string, string> {
  const out: Record<string, string> = {}
  for (const raw of text.split('\n')) {
    const line = raw.trim()
    if (!line) continue
    const [trainee, mentor, extra] = line.split('=').map((s) => s.trim().toLowerCase())
    if (!trainee || !mentor || extra !== undefined) throw new Error(`Mentor lines look like "trainee = mentor": ${line}`)
    out[trainee] = mentor
  }
  return out
}

export function formatMentors(m: Record<string, string>): string {
  return Object.entries(m).sort(([a], [b]) => a.localeCompare(b)).map(([t, s]) => `${t} = ${s}`).join('\n')
}
