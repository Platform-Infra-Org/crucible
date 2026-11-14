import { useEffect, useRef, useState, type KeyboardEvent, type PointerEvent } from 'react'
import { api } from '../api'
import type { AccountBlock, UnbilledWindow } from '../types'
import { toast } from '../lib/alerts'
import { usd } from '../lib/money'
import { at, cutFor, hhmm, markGestures, split, summary, type Drag, type MarkCtx } from '../lib/billedTimeline'
import { reportSaveError } from './Conflict'

const H = 3600_000
const MARKS = '/api/admin/account-spend/marks'

// BilledTimeline is the Ledger's last 24 hours: each paid lab's usage, hatched where the AWS bill already covers it and
// molten where Crucible counts it. Admins drag the green account marker (or a lab's own) to what AWS has billed.
export function BilledTimeline({ w, canMark, onSaved }: { w: UnbilledWindow; canMark: boolean; onSaved: (b: AccountBlock) => void }) {
  const from = Date.parse(w.from), to = Date.parse(w.to)
  const [drag, setDragState] = useState<Drag | null>(null)
  const [busy, setBusy] = useState(false) // a row button's or Count all's save is on its way
  const dragRef = useRef<Drag | null>(null)
  const busyRef = useRef(false)
  const plot = useRef<HTMLDivElement>(null)
  const [g] = useState(markGestures)
  useEffect(() => () => g.dispose(), [g]) // no save fires after the Ledger is gone
  const setDrag = (d: Drag | null) => { dragRef.current = d; setDragState(d) }

  const accountAt = drag?.key === '' ? drag.t : Math.max(w.account_mark ? Date.parse(w.account_mark) : from, from)
  const accountMark = drag?.key === '' ? new Date(drag.t).toISOString() : w.account_mark
  const own: Record<string, number> = drag && drag.key !== '' ? { [drag.key]: drag.t } : {}

  const put = async (body: object, done: string) => {
    try {
      onSaved(await api<AccountBlock>(MARKS, { method: 'PUT', json: body }))
      toast(done)
    } catch (e) {
      reportSaveError(e)
    }
  }
  // A moved marker: shown where it was dropped until the reply redraws it, or back where it was on a failure. Only this
  // drag is cleared, never one started meanwhile.
  const ctx: MarkCtx = {
    from, to, width: () => plot.current?.getBoundingClientRect().width ?? 0, get: () => dragRef.current, set: setDrag,
    save: (d) => void put({ lab_id: d.key, billed_to: new Date(d.t).toISOString() }, `Marked as billed up to ${hhmm(d.t)}.`)
      .finally(() => { if (dragRef.current === d) setDrag(null) }),
  }
  // A button's save: one at a time, so a double click sends once.
  const act = async (body: object, done: string) => {
    if (busyRef.current) return
    busyRef.current = true
    setBusy(true)
    await put(body, done)
    busyRef.current = false
    setBusy(false)
  }
  const slider = (key: string, t: number, label: string) => ({
    role: 'slider', tabIndex: 0, 'aria-label': label, 'aria-valuemin': from, 'aria-valuemax': to, 'aria-valuenow': t, 'aria-valuetext': hhmm(t),
    style: { left: `${at(t, from, to)}%` },
    onPointerDown: (e: PointerEvent<HTMLElement>) => { e.currentTarget.setPointerCapture(e.pointerId); g.down(ctx, key, t, e.clientX) },
    onPointerMove: (e: PointerEvent<HTMLElement>) => g.move(ctx, key, e.clientX),
    onPointerUp: () => g.up(ctx, key),
    onPointerCancel: () => g.cancel(ctx),
    onKeyDown: (e: KeyboardEvent<HTMLElement>) => { if (g.key(ctx, key, t, e.key)) e.preventDefault() },
    onKeyUp: (e: KeyboardEvent<HTMLElement>) => g.keyUp(ctx, e.key),
  })
  const reset = () => {
    if (window.confirm('Count all of the last 24 hours on top of the AWS bill? This removes every mark.')) void act({ reset: true }, 'Counting all of the last 24 hours.')
  }

  const sleptFrom = w.slept_from ? Math.max(Date.parse(w.slept_from), from) : undefined
  const sleptTo = w.slept_to ? Math.min(Date.parse(w.slept_to), to) : to
  const ticks = [0, 4, 8, 12, 16, 20, 24].map((h) => from + h * H)
  return (
    <section className={`panel billed-timeline${canMark ? '' : ' read-only'}`} aria-labelledby="billed-timeline-h">
      <div className="row billed-head">
        <h5 id="billed-timeline-h">Not yet on the AWS bill · last 24 hours</h5>
        <span className="spacer" />
        {canMark && <button type="button" className="ghost small" disabled={busy} onClick={reset}>Count all 24 h</button>}
      </div>
      <p role="status" className="billed-sum">{summary(w, accountMark, own)}</p>
      <p className="muted billed-how">
        {canMark ? "Check the month-to-date cost in the AWS console, then drag the green marker to the time it covers. Time left of a marker is already in the bill; Crucible adds only what's right of it."
          : 'An admin marks how far the AWS bill reaches; Crucible adds only the time after it.'}
      </p>
      {w.labs.length === 0 ? <p className="muted empty">No paid lab ran in the last 24 hours.</p> : (
        <div className="bt-chart">
          <div className="bt-row bt-axis-row" aria-hidden="true">
            <span className="bt-name" />
            <div className="bt-lane bt-axis">
              {ticks.map((t, i) => <span key={t} style={{ left: `${at(t, from, to)}%` }}>{i === ticks.length - 1 ? 'now' : hhmm(t)}</span>)}
            </div>
          </div>
          {w.labs.map((l) => {
            const mine = drag?.key === l.id || !!l.own_mark
            const cut = cutFor(l, accountMark, own, from)
            const name = `${l.training} (${l.requester})`
            return (
              <div key={l.id} className={`bt-row${mine ? ' own' : ''}`}>
                <div className="bt-name" title={`${name} · ${usd(l.hourly_usd)}/h`}>
                  {mine && <span className="bt-diamond" aria-hidden="true">◆ </span>}{l.training} <small className="muted">· {l.requester} · {usd(l.hourly_usd)}/h</small>
                </div>
                <div className="bt-lane">
                  {l.intervals.map((iv) => {
                    const s = split(iv, cut), f = Date.parse(iv.from), t = Date.parse(iv.to)
                    return (
                      <div key={iv.from} className="bt-bar" style={{ left: `${at(f, from, to)}%`, width: `${at(t, from, to) - at(f, from, to)}%` }}>
                        <span className="billed" style={{ width: `${s.billedFrac * 100}%` }} /><span className="hot" />
                      </div>
                    )
                  })}
                  {canMark && mine && <div className="bt-own" {...slider(l.id, cut, `Billed up to, ${name}, set on its own`)} />}
                </div>
                {canMark && (mine
                  ? <button type="button" className="ghost small bt-act" disabled={busy} onClick={() => void act({ lab_id: l.id, billed_to: null }, 'Following the account marker again.')}>Follow account</button>
                  : <button type="button" className="ghost small bt-act" disabled={busy} onClick={() => void act({ lab_id: l.id, billed_to: new Date(accountAt).toISOString() }, `Marked as billed up to ${hhmm(accountAt)}.`)}>Set separately</button>)}
              </div>
            )
          })}
          <div className="bt-over" ref={plot}>
            {sleptFrom !== undefined && sleptTo > sleptFrom && (
              <div className="bt-night" style={{ left: `${at(sleptFrom, from, to)}%`, width: `${at(sleptTo, from, to) - at(sleptFrom, from, to)}%` }}><em>forge asleep</em></div>
            )}
            {canMark && (
              <div className={`bt-marker${at(accountAt, from, to) < 15 ? ' start' : at(accountAt, from, to) > 85 ? ' end' : ''}`} {...slider('', accountAt, 'Account: AWS has billed usage up to')}>
                <span className="bt-handle">Account billed up to {hhmm(accountAt)} <span aria-hidden="true">⇆</span></span>
              </div>
            )}
          </div>
        </div>
      )}
      <ul className="bt-legend">
        <li><i className="sw billed" aria-hidden="true" />In the AWS bill</li>
        <li><i className="sw hot" aria-hidden="true" />Counted by Crucible</li>
        {canMark && <li><i className="sw marker" aria-hidden="true" />Account marker</li>}
        <li>◆ This lab set on its own</li>
        {sleptFrom !== undefined && <li><i className="sw night" aria-hidden="true" />Forge asleep</li>}
      </ul>
    </section>
  )
}
