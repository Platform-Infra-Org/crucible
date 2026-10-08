import { useEffect, useState } from 'react'
import { Link, useLocation } from 'react-router'
import { api } from '../api'
import { helpFor, type DocPage } from '../lib/docs'

let index: Promise<DocPage[]> | undefined // fetched once per page load
export const docIndex = () => (index ??= api<{ pages: DocPage[] }>('/api/docs').then((d) => d.pages).catch(() => { index = undefined; return [] }))

// HelpLink is the small "?" in the nav: the Docs page that covers the current route.
export function HelpLink() {
  const { pathname } = useLocation()
  const [pages, setPages] = useState<DocPage[]>([])
  useEffect(() => { docIndex().then(setPages) }, [])
  const page = helpFor(pages, pathname)
  if (!page || pathname.startsWith('/docs')) return null
  return <Link to={`/docs/${page.slug}`} className="help-link" aria-label="Help for this page" title={`Help: ${page.title}`}>?</Link>
}
