export const THEMES = [
  { id: 'forge', name: 'Forge', hint: 'Dark charcoal, molten ember' },
  { id: 'anvil', name: 'Anvil', hint: 'Light steel, iron blue', light: true },
  { id: 'quench', name: 'Quench', hint: 'Deep navy, cool cyan' },
  { id: 'damascus', name: 'Damascus', hint: 'Gunmetal, blued steel' },
  { id: 'verdigris', name: 'Verdigris', hint: 'Old bronze, green patina' },
  { id: 'starmetal', name: 'Starmetal', hint: 'Night sky, lilac, starlight' },
  { id: 'parchment', name: 'Parchment', hint: 'Vellum, oxblood ink', light: true },
  { id: 'contrast', name: 'High Contrast', hint: 'Maximum legibility' },
] as const

// isLight says whether a theme is light (dark text on a pale page), for things drawn outside CSS, like the editor.
export const isLight = (theme: string | undefined) => THEMES.some((t) => t.id === theme && 'light' in t)

export function applyTheme(theme: string, calm: boolean) {
  document.documentElement.dataset.theme = theme
  document.documentElement.dataset.calm = String(calm)
}
