// article is "a" or "an" for a rank name ("an Apprentice", "a Blade").
export const article = (t: string) => (/^[AEIOU]/i.test(t) ? 'an' : 'a')

export type RankUps = { smith: { level: number; title: string } | null; swords: { training: string; title: string; level: number; rank: string }[] }
export type Banner = { key: string; kind: 'smith' | 'sword'; trainingId?: string; trainingTitle?: string; title: string; level: number }

// nextBanner picks the one rank-up to show: the smith rise first, then each sword's in server order, skipping dismissed keys.
export function nextBanner(up: RankUps | undefined, done: Set<string>): Banner | null {
  if (!up) return null
  if (up.smith && !done.has('smith')) return { key: 'smith', kind: 'smith', title: up.smith.title, level: up.smith.level }
  const w = up.swords.find((s) => !done.has(`sword:${s.training}`))
  return w ? { key: `sword:${w.training}`, kind: 'sword', trainingId: w.training, trainingTitle: w.title, title: w.rank, level: w.level } : null
}

// seenBody is the POST /api/me/forge/seen body for a banner.
export const seenBody = (b: Banner) => ({ kind: b.kind, training: b.trainingId ?? '', level: b.level })
