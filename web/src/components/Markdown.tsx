import ReactMarkdown, { defaultUrlTransform, type Components } from 'react-markdown'
import remarkGfm from 'remark-gfm'
import rehypeHighlight from 'rehype-highlight'
import { remarkCallouts } from '../lib/callouts'
import { Mermaid } from './Mermaid'

const components: Components = {
  code({ className, children, ...rest }) {
    if (className?.includes('language-mermaid')) return <Mermaid source={String(children).trim()} />
    return <code className={className} {...rest}>{children}</code>
  },
}

// assetBase rewrites `assets/x.png` links to the training's asset endpoint.
// No raw HTML: react-markdown ignores it; rehype-highlight emits hast spans, never HTML strings.
export function Markdown({ text, assetBase }: { text: string; assetBase?: string }) {
  return (
    <div className="prose">
      <ReactMarkdown
        remarkPlugins={[remarkGfm, remarkCallouts]}
        rehypePlugins={[[rehypeHighlight, { plainText: ['mermaid'] }]]}
        components={components}
        urlTransform={(url) => (assetBase && url.startsWith('assets/') ? `${assetBase}/${url.slice('assets/'.length)}` : defaultUrlTransform(url))}
      >
        {text}
      </ReactMarkdown>
    </div>
  )
}
