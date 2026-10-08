import { useEffect, useRef, useState, type CSSProperties } from 'react'
import { Link } from 'react-router'
import { useMe } from '../me'
import { useFetch } from '../useFetch'
import type { TeamSummary } from '../types'
import { reportSaveError } from './Conflict'
import { Avatar, Icon } from './Avatar'
import { AVATARS, initials } from '../lib/avatars'

// Motes of light carried along the molten band, each on its own: where it rests under calm motion (x, y in %), its size,
// how long it takes to cross the band (d), and its own bob (amp px over b s) and twinkle (t s). The drift starts at x,
// so the motes are spread out from the first frame, and is linear, so the flow never pauses.
const MOTES = [
  { x: 6, y: 34, s: 3, d: 9, amp: 4, b: 2.3, t: 1.7 }, { x: 15, y: 62, s: 2, d: 13, amp: 3, b: 3.1, t: 2.4 },
  { x: 24, y: 20, s: 3.5, d: 7.5, amp: 5, b: 2.7, t: 1.3 }, { x: 33, y: 48, s: 2, d: 11, amp: 3, b: 1.9, t: 2.9 },
  { x: 42, y: 72, s: 2.5, d: 14, amp: 4, b: 3.4, t: 2.1 }, { x: 51, y: 28, s: 2, d: 10, amp: 6, b: 2.5, t: 1.5 },
  { x: 60, y: 55, s: 3, d: 8.5, amp: 3, b: 2.1, t: 2.6 }, { x: 70, y: 16, s: 2, d: 12, amp: 4, b: 2.9, t: 1.9 },
  { x: 79, y: 42, s: 4, d: 6.5, amp: 5, b: 3.6, t: 2.2 }, { x: 89, y: 66, s: 2.5, d: 11.5, amp: 3, b: 2.2, t: 1.6 },
]

// UserMenu is the person's icon and name at the right of the top bar. It opens their user card: who they are, the
// teams they are on (each a link to its page), the icon they show, and the way to Settings and out.
export function UserMenu() {
  const { me, setAvatar } = useMe()
  const [open, setOpen] = useState(false)
  const [busy, setBusy] = useState(false)
  const box = useRef<HTMLDivElement>(null)
  const button = useRef<HTMLButtonElement>(null)
  const teams = useFetch<TeamSummary[]>(open ? '/api/teams' : null)
  const u = me.user

  useEffect(() => {
    if (!open) return
    const onDown = (e: MouseEvent) => {
      if (!box.current?.contains(e.target as Node)) setOpen(false)
    }
    document.addEventListener('mousedown', onDown)
    return () => document.removeEventListener('mousedown', onDown)
  }, [open])

  const close = () => {
    setOpen(false)
    button.current?.focus()
  }
  const pick = async (avatar: string) => {
    if (busy || avatar === u.avatar) return
    setBusy(true)
    try { await setAvatar(avatar) } catch (e) { reportSaveError(e) } finally { setBusy(false) }
  }
  const logout = async () => {
    await fetch('/auth/logout', { method: 'POST' })
    window.location.href = '/auth/login'
  }

  return (
    <div className="user-menu" ref={box} onKeyDown={(e) => { if (e.key === 'Escape' && open) { e.stopPropagation(); close() } }}>
      <button ref={button} type="button" className="user-button" aria-haspopup="dialog" aria-expanded={open} onClick={() => setOpen(!open)}>
        <Avatar user={u} />
        <span>{u.name || u.email}</span>
      </button>
      {open && (
        <div className="user-card" role="dialog" aria-label="Your user card">
          <div className="user-card-band" aria-hidden="true">
            {MOTES.map((m, i) => (
              <span key={i} className="mote" style={{ '--x': m.x, '--y': `${m.y}%`, '--s': `${m.s}px`, '--d': `${m.d}s`, '--delay': `${-m.d * m.x / 100}s`,
                '--amp': `${m.amp}px`, '--b': `${m.b}s`, '--t': `${m.t}s` } as CSSProperties} />
            ))}
          </div>
          <div className="user-card-head">
            <Avatar user={u} large />
            <div>
              <strong>{u.name || u.email}</strong>
              <span className="muted">{u.email}</span>
              {me.is_admin && <span className="badge">admin</span>}
            </div>
          </div>

          <h3>Teams</h3>
          {teams.error && <p className="error">Your teams could not be loaded.</p>}
          {!teams.error && !teams.data && <p className="muted">Gathering your teams…</p>}
          {teams.data?.length === 0 && <p className="muted">You&apos;re not on a team yet.</p>}
          {teams.data && teams.data.length > 0 && (
            <ul className="user-card-teams">
              {teams.data.map((t) => (
                <li key={t.id}>
                  <Link to={`/teams/${t.id}`} onClick={() => setOpen(false)}>{t.name}</Link>
                  <span className="badge">{t.role}</span>
                </li>
              ))}
            </ul>
          )}

          <h3 id="icon-label">Your icon</h3>
          <div className="icon-picker" role="group" aria-labelledby="icon-label">
            <button type="button" aria-pressed={!u.avatar} disabled={busy} title="Initials" aria-label="Initials" onClick={() => void pick('')}>
              <span className="avatar-initials">{initials(u)}</span>
            </button>
            {AVATARS.map((a) => (
              <button key={a.id} type="button" aria-pressed={u.avatar === a.id} disabled={busy} title={a.label} aria-label={a.label}
                onClick={() => void pick(a.id)}>
                <Icon id={a.id} />
              </button>
            ))}
          </div>

          <div className="row user-card-foot">
            <Link to="/settings" onClick={() => setOpen(false)}>Settings</Link>
            <span className="spacer" />
            <button type="button" className="ghost" onClick={logout}>Log out</button>
          </div>
        </div>
      )}
    </div>
  )
}
