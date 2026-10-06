import type { Feedback } from '../types'

const kb = (n: number) => (n < 1024 ? `${n} B` : `${Math.ceil(n / 1024)} KiB`)

// What a trainee sees of their own human-scored work. Feedback is plain text (pre-wrap), never HTML or Markdown.
export function FeedbackBox({ f }: { f: Feedback }) {
  const label = { pending: 'Waiting for a scorer', scored: `Scored ${f.points} / ${f.max_points}`, returned: 'Returned for rework' }[f.status]
  return (
    <div className={`feedback ${f.status}`} role="status" data-testid="feedback">
      <strong>{label}</strong>
      {f.scored_by && f.status !== 'pending' && <span className="muted"> · {f.scored_by}</span>}
      {f.feedback && <p className="pre">{f.feedback}</p>}
      {f.answer && <p className="muted pre">You wrote: {f.answer}</p>}
      {(f.files ?? []).map((file, i) => (
        <p key={i}><a href={`/api/submissions/${f.id}/files/${i}`}>{file.name}</a> <span className="muted">({kb(file.size)})</span></p>
      ))}
    </div>
  )
}
