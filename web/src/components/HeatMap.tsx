import type { Heat, JourneyFlag, JourneyRow } from '../types'
import { useCalm } from '../me'

export const glyph: Record<Heat, string> = { cold: '·', glowing: '◐', forged: '●' }
export const heatText: Record<Heat, string> = { cold: 'not started', glowing: 'in progress', forged: 'complete' }
const why: Record<JourneyFlag['kind'], string> = {
  failed_checks: 'May be stuck: ', final_hint: 'May need help: ', inactive: 'Gone quiet: ', returned_twice: 'Work keeps coming back: ',
}

export function Legend() {
  return (
    <ul className="legend" aria-label="Legend">
      {(Object.keys(glyph) as Heat[]).map((h) => <li key={h}><span className={`heat heat-${h}`} aria-hidden="true">{glyph[h]}</span> {h} ({heatText[h]})</li>)}
    </ul>
  )
}

// Cells are a plain list with the module name as visible text: no hover needed, no per-cell tab stops.
export function HeatMap({ rows, level = 2 }: { rows: JourneyRow[]; level?: 2 | 3 }) {
  const H = `h${level}` as 'h2' | 'h3'
  const calm = useCalm()
  if (rows.length === 0) return <p className="muted">Nothing to show for you here yet.</p>
  return (
    <div className="journey">
      {rows.map((r) => {
        const n = (h: Heat) => r.cells.filter((c) => c.heat === h).length
        return (
          <section key={r.email + r.training} className="journey-row" aria-label={`${r.name || r.email} in ${r.title}`} data-testid={`journey-${r.email}-${r.training}`}>
            <H>{r.name || r.email} <span className="muted">· {r.title} · {r.percent}% forged</span></H>
            <ol className={`heat-cells${calm ? ' calm' : ''}`}>
              {r.cells.map((c) => (
                <li key={c.module} className="heat-cell">
                  <span className={`heat heat-${c.heat}`} aria-hidden="true">{glyph[c.heat]}</span>
                  <span>{c.title}<span className="sr-only">: {c.heat}</span></span>
                </li>
              ))}
            </ol>
            <p className="heat-summary muted">{n('forged')} complete, {n('glowing')} in progress, {n('cold')} not started</p>
            {r.flags.length > 0 && (
              <ul className="flags">
                {r.flags.map((f, i) => <li key={i}><span role="note" className="warn"><span aria-hidden="true">⚠ </span>{why[f.kind] ?? ''}{f.detail}{f.module ? ` (${f.module})` : ''}</span></li>)}
              </ul>
            )}
          </section>
        )
      })}
    </div>
  )
}
