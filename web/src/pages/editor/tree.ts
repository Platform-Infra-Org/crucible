// The Explorer's folder tree, built from the draft's flat file list. Folder paths have no trailing slash.
export type TreeNode =
  | { kind: 'folder'; name: string; path: string; children: TreeNode[] }
  | { kind: 'file'; name: string; path: string; reason?: string } // reason: why it can't be edited here (greyed)
export type Row = { node: TreeNode; depth: number; parent: string }

const order = (a: TreeNode, b: TreeNode) =>
  a.kind !== b.kind ? (a.kind === 'folder' ? -1 : 1) : a.name.localeCompare(b.name, 'en', { numeric: true })

export function buildTree(paths: string[], greyed: { path: string; reason: string }[] = []): TreeNode[] {
  const root: TreeNode[] = []
  const add = (path: string, reason?: string) => {
    let level = root
    const parts = path.split('/')
    parts.forEach((name, i) => {
      if (i === parts.length - 1) { level.push({ kind: 'file', name, path, reason }); return }
      const dir = parts.slice(0, i + 1).join('/')
      let f = level.find((n) => n.kind === 'folder' && n.path === dir)
      if (!f) { f = { kind: 'folder', name, path: dir, children: [] }; level.push(f) }
      level = (f as Extract<TreeNode, { kind: 'folder' }>).children
    })
  }
  for (const p of paths) add(p)
  for (const g of greyed) add(g.path, g.reason)
  const sort = (ns: TreeNode[]) => { ns.sort(order); for (const n of ns) if (n.kind === 'folder') sort(n.children) }
  sort(root)
  return root
}

// rows are the tree's visible rows, top to bottom: nothing under a collapsed folder.
export function rows(tree: TreeNode[], collapsed: Set<string>, depth = 0, parent = ''): Row[] {
  return tree.flatMap((node) => [
    { node, depth, parent },
    ...(node.kind === 'folder' && !collapsed.has(node.path) ? rows(node.children, collapsed, depth + 1, node.path) : []),
  ])
}

export const ancestors = (path: string) => path.split('/').slice(0, -1).map((_, i, a) => a.slice(0, i + 1).join('/'))
export const dirOf = (path: string) => path.slice(0, path.lastIndexOf('/') + 1)
export const iconOf = (path: string) => (/\.md$/.test(path) ? 'md' : /\.ya?ml$/.test(path) ? 'yaml' : /\.sh$/.test(path) ? 'sh' : 'file')

// menuFor is what a row's context menu offers. training.yaml and greyed files are never renamed or deleted here.
export function menuFor(node: TreeNode, readOnly: boolean): ('new' | 'rename' | 'delete')[] {
  if (readOnly) return []
  if (node.kind === 'folder' || node.reason !== undefined || node.path === 'training.yaml') return ['new']
  return ['new', 'rename', 'delete']
}

// focusAfterDelete is the row focus returns to once a Delete is answered: the row itself when cancelled, else its
// neighbour (the next row, or the previous one at the end).
export function focusAfterDelete(rs: Row[], path: string, deleted: boolean): string | undefined {
  if (!deleted) return path
  const i = rs.findIndex((r) => r.node.path === path)
  return (rs[i + 1] ?? rs[i - 1])?.node.path
}

// treeKey maps a key on the focused row to what the tree does (the WAI-ARIA tree pattern); undefined: not a tree key.
export function treeKey(rs: Row[], at: string, key: string, collapsed: Set<string>): { focus?: string; expand?: string; collapse?: string; open?: string } | undefined {
  const i = rs.findIndex((r) => r.node.path === at)
  const r = rs[i]
  if (!r) return undefined
  const folder = r.node.kind === 'folder'
  const open = folder && !collapsed.has(at)
  const to = (j: number) => (rs[j] ? { focus: rs[j].node.path } : {})
  switch (key) {
    case 'ArrowDown': return to(i + 1)
    case 'ArrowUp': return to(i - 1)
    case 'Home': return to(0)
    case 'End': return to(rs.length - 1)
    case 'ArrowRight': return !folder ? {} : open ? to(i + 1) : { expand: at }
    case 'ArrowLeft': return open ? { collapse: at } : r.parent ? { focus: r.parent } : {}
    case 'Enter': return folder ? (open ? { collapse: at } : { expand: at }) : r.node.kind === 'file' && r.node.reason === undefined ? { open: at } : {}
    default: return undefined
  }
}
