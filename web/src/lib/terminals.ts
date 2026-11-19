// The lab's terminal tabs. A tab is one connection: closing it ends the shell behind it, which is how a trainee frees
// a serial console (AWS allows one session per machine at a time) and opens a fresh one.
export type Tab = { key: string; name: string }

// openTab adds a terminal of `name`, keeping its first tab's key the plain name so the lab's own terminals read as they
// are written in lab.yaml; further copies are "name #2", "name #3", reusing a number a closed tab gave back.
export function openTab(tabs: Tab[], name: string): Tab {
  let key = name
  for (let n = 2; tabs.some((t) => t.key === key); n++) key = `${name} #${n}`
  return { key, name }
}

// closeTab drops `key` and says which tab to show instead: the one that took its place, or the last one.
export function closeTab(tabs: Tab[], key: string): { tabs: Tab[]; active: string } {
  const i = tabs.findIndex((t) => t.key === key)
  const left = tabs.filter((t) => t.key !== key)
  return { tabs: left, active: left[Math.min(i, left.length - 1)]?.key ?? '' }
}

// terminalOf says which of the lab's terminals a tab key belongs to ("console #2" -> "console").
export function terminalOf(key: string, declared: string[]): string {
  return declared.find((n) => key === n || key.startsWith(`${n} #`)) ?? key
}

// initialTabs is what the page opens with: the lab's own terminals, plus any further tab still running on the server
// (a session outlives the page, so a refresh or a walk to another page comes back to the same shells).
export function initialTabs(declared: string[], open: string[]): Tab[] {
  const extra = open.filter((k) => !declared.includes(k)).sort()
  return [...declared, ...extra].map((key) => ({ key, name: terminalOf(key, declared) }))
}
