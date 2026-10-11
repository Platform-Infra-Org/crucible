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

const offered = (contexts: KubeContext[], name: string) => name !== '' && contexts.some((c) => c.name === name)

// keepPick keeps the current pick while the agent still reports it; a context that vanished un-picks (preselect again).
export function keepPick(contexts: KubeContext[], current: string, remembered: string | null): string {
  return offered(contexts, current) ? current : pickContext(contexts, remembered)
}

// clusterReady: a cluster lab starts only on a context the agent reports now, and a cluster-wide one only with consent.
export function clusterReady(contexts: KubeContext[], pick: string, clusterWide: boolean, consent: boolean): boolean {
  return offered(contexts, pick) && (!clusterWide || consent)
}

// keepAsking: the lab page re-asks while the agent is away, and for a cluster lab while there is no usable pick (the
// agent may connect before it reports its contexts, and a new context may appear), so nothing needs a reload.
export function keepAsking(info: { runtime: string; runtime_ready: boolean; contexts?: KubeContext[] }, pick: string): boolean {
  return !info.runtime_ready || (info.runtime === 'cluster' && !offered(info.contexts ?? [], pick))
}
