const norm = (s: string) => s.replace(/\r\n/g, '\n')

export const byteLen = (s: string) => new TextEncoder().encode(s).length

// A textarea normalises line endings to LF; give a file that was CRLF its endings back.
export const restoreEol = (orig: string, text: string) => (orig.includes('\r\n') ? norm(text).replace(/\n/g, '\r\n') : text)

// changedFiles is what gets submitted: files whose text differs from the head, with the original line endings.
export function changedFiles(orig: Record<string, string>, files: Record<string, string>): Record<string, string> {
  const out: Record<string, string> = {}
  for (const [p, t] of Object.entries(files)) {
    const o = orig[p]
    if (o === undefined) out[p] = t
    else if (norm(o) !== norm(t)) out[p] = restoreEol(o, t)
  }
  return out
}

// headVersions loads the current head text of each path; paths that can't be loaded (new in the old edit) are left out.
export async function headVersions(paths: string[], load: (p: string) => Promise<string>): Promise<Record<string, string>> {
  const out: Record<string, string> = {}
  await Promise.all(paths.map(async (p) => { try { out[p] = await load(p) } catch { /* not at head */ } }))
  return out
}
