import { lazy, Suspense } from 'react'

// react-markdown + highlight.js are a big share of the bundle: load them only on pages that render markdown.
const Impl = lazy(() => import('./MarkdownImpl').then((m) => ({ default: m.MarkdownImpl })))

export function Markdown(props: { text: string; assetBase?: string }) {
  return <Suspense fallback={<div className="prose" aria-busy="true" />}><Impl {...props} /></Suspense>
}
