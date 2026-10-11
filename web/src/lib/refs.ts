// Role holders are people (emails) or groups: all, all_seniors, <team>, <team>_seniors, … (see the server's
// config.Platform.Members).

export const isGroup = (ref: string) => !ref.includes('@')

const plural: Record<string, string> = { leaders: 'leaders', seniors: 'seniors', juniors: 'juniors', trainees: 'trainees' }

// describeGroup says who a group is, in words: "every senior of platform", "everyone on every team".
export function describeGroup(ref: string): string {
  const m = /^(.+)_(leaders|seniors|juniors|trainees)$/.exec(ref)
  if (ref === 'all') return 'everyone on every team'
  if (!m) return `everyone on ${ref}`
  const who = plural[m[2]]
  return m[1] === 'all' ? `the ${who} of every team` : `the ${who} of ${m[1]}`
}

// checkRef reads what someone typed into a role picker: an email, or a group that exists.
export function checkRef(raw: string, groups: string[]): { ref: string } | { problem: string } {
  const ref = raw.trim().toLowerCase()
  if (!ref) return { problem: 'Type an email or pick a group.' }
  if (ref.includes('@')) return /^[^\s@]+@[^\s@]+$/.test(ref) ? { ref } : { problem: `${ref} is not an email address.` }
  if (groups.includes(ref)) return { ref }
  return { problem: `${ref} is not a group. Groups look like all, all_seniors, platform or platform_seniors.` }
}
