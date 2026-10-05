export const THEMES = [
  { id: 'forge', name: 'Forge', hint: 'Dark charcoal, molten ember' },
  { id: 'anvil', name: 'Anvil', hint: 'Light steel, iron blue' },
  { id: 'quench', name: 'Quench', hint: 'Deep navy, cool cyan' },
  { id: 'contrast', name: 'High Contrast', hint: 'Maximum legibility' },
] as const

export function applyTheme(theme: string, calm: boolean) {
  document.documentElement.dataset.theme = theme
  document.documentElement.dataset.calm = String(calm)
}
