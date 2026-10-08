import type { User } from '../types'

// The forge icons a person can pick for their user card; the server keeps the same list (auth.Avatars).
// Each is drawn in a 24×24 box as poured metal (components/Avatar.tsx): `body` is filled with the molten gradient of the
// theme's accents, `strokes` are drawn with it, and `core` is the white-hot heart laid on top.
export const AVATARS: { id: string; label: string; body?: string; strokes?: string; core?: string }[] = [
  { id: 'hammer', label: 'Hammer',
    body: 'M9.2 7.4l4.9-4.9a1 1 0 0 1 1.4 0l4 4a1 1 0 0 1 0 1.4l-4.9 4.9a1 1 0 0 1-1.4 0l-4-4a1 1 0 0 1 0-1.4z M11.3 11.9l1.8 1.8-8 8a1.27 1.27 0 0 1-1.8-1.8z',
    core: 'M14.8 4.2l3.6 3.6-.9.9-3.6-3.6z' },
  { id: 'anvil', label: 'Anvil',
    body: 'M2 6.5h15.5c2.4 0 4.3 1.3 5 3.5h-6.3l-1.7 2.6V15h1.4l1.6 4H6.5l1.6-4h1.4v-2.4L7.3 10.2C4.3 10 2 8.8 2 6.5z',
    core: 'M4 7.6h12.6c1.2 0 2.2.3 3 .9H6.6C5.4 8.5 4.5 8.2 4 7.6z' },
  { id: 'flame', label: 'Flame',
    body: 'M12 2.5c1 3.8 6.8 6 6.8 11.7a6.8 6.8 0 0 1-13.6 0c0-3 1.8-5.2 3.4-6.4 0 2.2 1 3.6 2.4 3.6-.4-3-.6-5.8 1-8.9z',
    core: 'M12 20.6c-1.9 0-3.3-1.3-3.3-3.1 0-1.7 1.3-2.9 2.3-3.8.2 1.1.9 1.8 1.7 1.8.3-1 .1-2 .6-2.9 1.3 1.2 2 2.5 2 4.2 0 2.1-1.4 3.8-3.3 3.8z' },
  { id: 'sword', label: 'Sword',
    body: 'M21 3l-.9 4.6-8.7 8.7-3.7-3.7 8.7-8.7z M4.6 11.4l1.4-1.4 8 8-1.4 1.4z M7.4 15.4l1.2 1.2-4.2 4.2a.85.85 0 0 1-1.2-1.2z',
    core: 'M19.6 4.4l-.4 2.1-7.8 7.8-.9-.9 7.8-7.8z' },
  { id: 'shield', label: 'Shield',
    body: 'M12 2.5l8.5 3.2v6.1c0 5.2-3.6 8.6-8.5 9.7-4.9-1.1-8.5-4.5-8.5-9.7V5.7z',
    core: 'M12 5.3l5.9 2.2v4.3c0 3.7-2.4 6.2-5.9 7.2z' },
  { id: 'tongs', label: 'Tongs',
    strokes: 'M6.5 21l5.5-9.5 2-6.2 1.8-2.3 M17.5 21L12 11.5l-2-6.2-1.8-2.3',
    core: 'M10.6 11.5a1.4 1.4 0 1 0 2.8 0a1.4 1.4 0 1 0-2.8 0z' },
  { id: 'helm', label: 'Helm',
    body: 'M4 20.5v-8a8 8 0 0 1 16 0v8h-6.2v-5.3h-3.6v5.3z M6.6 11h10.8v1.7H6.6z',
    core: 'M7.2 8.6a5.6 5.6 0 0 1 4-3.4v1.6a4 4 0 0 0-2.6 2.4z' },
  { id: 'ingot', label: 'Ingot',
    body: 'M2.5 18.5l3.2-7.5h12.6l3.2 7.5z',
    core: 'M5.7 11l2.4-4h7.8l2.4 4z' },
]

export const initials = (u: Pick<User, 'name' | 'email'>) => {
  const words = (u.name || u.email.split('@')[0]).split(/[\s._-]+/).filter(Boolean)
  return (words.length > 1 ? words[0][0] + words[words.length - 1][0] : (words[0] ?? '?').slice(0, 2)).toUpperCase()
}
