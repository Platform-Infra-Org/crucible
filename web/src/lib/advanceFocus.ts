// After a passed check moves on, the focused Check button unmounts. This picks where focus lands instead:
// the next task's pip (its aria-label names the task) plus a message for the persistent live region.
export type PipTask = { id: string; title: string }

export function advanceFocus(tasks: PipTask[], fromId: string, nextId: string): { index: number; message: string } | null {
  if (nextId === fromId) return null // last task: nothing moved, the "Passed" line is already announced
  const index = tasks.findIndex((t) => t.id === nextId)
  if (index < 0) return null
  return { index, message: `Task ${index + 1} of ${tasks.length} is next` }
}

// Ctrl+Alt+Up is eaten by some OS shortcuts (GNOME workspaces, Windows display rotation), so Ctrl+Shift+F6 also leaves.
export function isLeaveChord(e: { ctrlKey: boolean; altKey: boolean; shiftKey: boolean; key: string }): boolean {
  return e.ctrlKey && ((e.altKey && !e.shiftKey && e.key === 'ArrowUp') || (e.shiftKey && !e.altKey && e.key === 'F6'))
}
