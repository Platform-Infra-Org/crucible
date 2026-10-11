import type { KubeContext } from '../types'

const KEY = 'crucible.kubeContext'

// rememberContext keeps the last cluster picked, in this browser only (a convenience; the agent's list is the truth).
export function rememberContext(name: string) {
  try { localStorage.setItem(KEY, name) } catch { /* private window */ }
}

export function rememberedContext(): string | null {
  try { return localStorage.getItem(KEY) } catch { return null }
}

// pickContext preselects the remembered context if the agent still reports it, or the only one there is.
export function pickContext(contexts: KubeContext[], remembered: string | null): string {
  if (remembered && contexts.some((c) => c.name === remembered)) return remembered
  return contexts.length === 1 ? contexts[0].name : ''
}
