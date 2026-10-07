import { useEffect, useState } from 'react'
import { Link, Navigate, useParams } from 'react-router'
import { useMe } from '../me'
import { useFetch } from '../useFetch'
import { docSections, landing, rolesOf, searchDocs, type DocPage } from '../lib/docs'
import { docIndex } from '../components/HelpLink'
import { ErrorBox } from '../components/ErrorBox'
import { Loader } from '../components/Loader'
import { Markdown } from '../components/Markdown'

export function DocsPage() {
  const slug = useParams()['*'] ?? ''
  const { me } = useMe()
  const roles = rolesOf(me)
  const [pages, setPages] = useState<DocPage[]>()
  const [all, setAll] = useState(false)
  const [q, setQ] = useState('')
  useEffect(() => { docIndex().then(setPages) }, [])
  const page = useFetch<{ title: string; markdown: string; version: string }>(slug ? `/api/docs/${slug}` : null)
  if (!pages) return <Loader label="Opening the docs…" />
  if (!slug) {
    const to = landing(pages, roles)
    return to ? <Navigate to={`/docs/${to}`} replace /> : <section className="page"><p>No docs yet.</p></section>
  }
  const found = new Set(searchDocs(pages, q).map((p) => p.slug))
  return (
    <section className="page docs">
      <nav className="docs-nav" aria-label="Docs">
        <h2 id="docs-search" className="docs-search-title">Search the docs</h2>
        <input type="search" aria-labelledby="docs-search" value={q} onChange={(e) => setQ(e.target.value)} />
        <label className="switch">
          <input type="checkbox" role="switch" checked={all} onChange={(e) => setAll(e.target.checked)} />
          <span className="switch-track" aria-hidden="true" />
          <span className="switch-label">Show all sections</span>
        </label>
        {docSections(pages, roles, all || q !== '').map((s) => {
          const list = s.pages.filter((p) => found.has(p.slug))
          return list.length > 0 && (
            <div key={s.id}>
              <h2>{s.title}</h2>
              <ul>{list.map((p) => <li key={p.slug}><Link to={`/docs/${p.slug}`} aria-current={p.slug === slug ? 'page' : undefined}>{p.title}</Link></li>)}</ul>
            </div>
          )
        })}
      </nav>
      <article>
        {page.error ? <ErrorBox error={page.error} /> : !page.data ? <Loader label="Reading…" /> : (
          <>
            <Markdown text={page.data.markdown} />
            <p className="muted">Last updated with Crucible {page.data.version}</p>
          </>
        )}
      </article>
    </section>
  )
}
