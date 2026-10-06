// Client-side mirror of gitsync.CheckEditFiles; the server stays the authority.
export const MAX_EDIT_FILES = 20
export const MAX_EDIT_FILE_BYTES = 256 << 10
const exts = ['.md', '.yaml', '.yml', '.sh']

export function editProblem(files: Record<string, string>): string | undefined {
  const paths = Object.keys(files)
  if (paths.length === 0 || paths.length > MAX_EDIT_FILES) return `An edit changes 1 to ${MAX_EDIT_FILES} files.`
  for (const p of paths) {
    if (p !== 'training.yaml' && !/^modules\/[^/]+\/.+/.test(p)) return `${p}: only training.yaml and files under modules/<id>/ can be edited.`
    if (p.split('/').includes('..')) return `${p}: not a valid path.`
    if (!exts.some((e) => p.toLowerCase().endsWith(e))) return `${p}: only .md, .yaml, .yml and .sh files can be edited here.`
    if (new TextEncoder().encode(files[p]).length > MAX_EDIT_FILE_BYTES || files[p].includes('\0')) return `${p}: must be text of at most 256 KiB.`
  }
}
