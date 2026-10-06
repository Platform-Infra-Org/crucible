type MdNode = { type: string; value?: string; children?: MdNode[]; data?: { hProperties?: Record<string, string> } }
const CALLOUT = /^\[!(NOTE|TIP|IMPORTANT|WARNING|CAUTION)\]\s*/

// remarkCallouts turns GitHub-style `> [!NOTE]` blockquotes into styled callouts (spec §7).
export function remarkCallouts() {
  return (tree: MdNode) => {
    const walk = (n: MdNode) => {
      if (n.type === 'blockquote') {
        const p = n.children?.[0]
        const t = p?.type === 'paragraph' ? p.children?.[0] : undefined
        const m = t?.type === 'text' ? CALLOUT.exec(t.value ?? '') : null
        if (m && t) {
          t.value = (t.value ?? '').slice(m[0].length)
          n.data = { hProperties: { className: `callout callout-${m[1].toLowerCase()}`, 'data-callout': m[1] } }
        }
      }
      n.children?.forEach(walk)
    }
    walk(tree)
  }
}
