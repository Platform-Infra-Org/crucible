import type { Problem } from '../../types'

export function ProblemsPanel({ problems, known, onJump }: { problems: Problem[]; known: (file: string) => boolean; onJump: (p: Problem) => void }) {
  if (problems.length === 0) return <p className="muted" role="status">No problems. The training loads and lints clean.</p>
  return (
    <ul className="problems" aria-label="Problems">
      {problems.map((p, i) => (
        <li key={i}>
          {known(p.file)
            ? <button className="ghost" onClick={() => onJump(p)}><code>{`${p.file}:${p.line}`}</code> {p.msg}</button>
            : <span><code>{p.file}</code> {p.msg}</span>}
        </li>
      ))}
    </ul>
  )
}
