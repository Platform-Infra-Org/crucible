// The server answers 409 for two different things; say which one happened.
export function conflictNotice(msg: string): string {
  if (/changed since it was reviewed/.test(msg)) return `Edit moved: ${msg} Nothing was merged; read the new diff below and approve again.`
  if (/no longer applies/.test(msg)) return `No longer applies — redo it: ${msg}`
  return msg
}
