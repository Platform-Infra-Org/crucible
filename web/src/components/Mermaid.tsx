import { useEffect, useId, useState } from 'react'

// `secure` replaces mermaid's default list, so the defaults are repeated; the extra keys stop an in-diagram
// %%{init}%% directive from re-enabling HTML labels or injecting CSS.
export function mermaidConfig(dark: boolean) {
  return {
    startOnLoad: false,
    securityLevel: 'strict' as const,
    theme: dark ? ('dark' as const) : ('default' as const),
    flowchart: { htmlLabels: false },
    secure: ['secure', 'securityLevel', 'startOnLoad', 'maxTextSize', 'suppressErrorRendering', 'maxEdges',
      'htmlLabels', 'flowchart', 'themeCSS', 'themeVariables', 'fontFamily'],
  }
}

// Mermaid renders a diagram on the client; mermaid is imported only when a page has one.
// Diagram source is untrusted: strict security level (mermaid sanitizes labels and blocks click handlers),
// and flowchart labels are SVG text, not HTML.
export function Mermaid({ source }: { source: string }) {
  const id = 'mmd' + useId().replace(/[^a-zA-Z0-9]/g, '')
  const [svg, setSvg] = useState<string>()
  const [failed, setFailed] = useState(false)
  useEffect(() => {
    let live = true
    import('mermaid')
      .then(async ({ default: m }) => {
        const dark = document.documentElement.dataset.theme !== 'anvil'
        m.initialize(mermaidConfig(dark))
        const { svg } = await m.render(id, source)
        if (live) setSvg(svg)
      })
      .catch(() => live && setFailed(true))
    return () => {
      live = false
    }
  }, [id, source])
  if (!svg) return <pre className={failed ? 'mermaid-failed' : 'mermaid-pending'}>{source}</pre>
  return <div className="mermaid" role="img" aria-label="Diagram" dangerouslySetInnerHTML={{ __html: svg }} />
}
