import { marked } from '../lib/marked'

const kind = (l: string) =>
  l.startsWith('@@') ? 'diff-hunk' : l.startsWith('+') && !l.startsWith('+++') ? 'diff-add' : l.startsWith('-') && !l.startsWith('---') ? 'diff-del' : 'diff-ctx'

export function DiffView({ diff }: { diff: string }) {
  return (
    <figure role="region" aria-label="Changes" style={{ margin: 0 }}>
      <pre className="diff" data-testid="diff">
        {diff.split('\n').map((l, i) => <div key={i} className={kind(l)}>{l === '' ? ' ' : marked(l, l !== ' ')}</div>)}
      </pre>
    </figure>
  )
}
