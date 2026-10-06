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

export function HeatMap({ rows }: { rows: JourneyRow[] }) {
  const calm = useCalm()
  if (rows.length === 0) return <p className="muted">Nobody to show here yet.</p>
  return (
    <div className="journey">
      {rows.map((r) => {
        const n = (h: Heat) => r.cells.filter((c) => c.heat === h).length
        return (
          <section key={r.email + r.training} className="journey-row" aria-label={`${r.name || r.email} in ${r.title}`} data-testid={`journey-${r.email}-${r.training}`}>
            <h3>{r.name || r.email} <span className="muted">· {r.title} · {r.percent}% forged</span></h3>
            <ol className={`heat-cells${calm ? ' calm' : ''}`}>
              {r.cells.map((c) => (
                <li key={c.module} tabIndex={0} className={`heat heat-${c.heat}`} aria-label={`${c.title}: ${c.heat}`} title={`${c.title}: ${c.heat}`}>
                  <span aria-hidden="true">{glyph[c.heat]}</span>
                </li>
              ))}
            </ol>
            <p className="heat-summary muted">{n('forged')} complete, {n('glowing')} in progress, {n('cold')} not started</p>
            {r.flags.length > 0 && (
              <ul className="flags">
                {r.flags.map((f, i) => <li key={i} role="note" className="warn">⚠ {why[f.kind] ?? ''}{f.detail}{f.module ? ` (${f.module})` : ''}</li>)}
              </ul>
            )}
          </section>
        )
      })}
    </div>
  )
}
