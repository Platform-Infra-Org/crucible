import ReactMarkdown, { defaultUrlTransform } from 'react-markdown'
import remarkGfm from 'remark-gfm'

// assetBase rewrites `assets/x.png` links to the training's asset endpoint.
export function Markdown({ text, assetBase }: { text: string; assetBase?: string }) {
  return (
    <div className="prose">
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        urlTransform={(url) => (assetBase && url.startsWith('assets/') ? `${assetBase}/${url.slice('assets/'.length)}` : defaultUrlTransform(url))}
      >
        {text}
      </ReactMarkdown>
    </div>
  )
}
