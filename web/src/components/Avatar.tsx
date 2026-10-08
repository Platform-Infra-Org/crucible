import { useId } from 'react'
import type { User } from '../types'
import { AVATARS, initials } from '../lib/avatars'

// Icon pours one forge icon in molten metal: the theme's accent-2 at the top cooling to accent at the bottom, with a
// white-hot core. The gradient is styled through CSS variables, so every theme recolours it.
export function Icon({ id }: { id: string }) {
  const g = 'molten' + useId().replace(/[^a-zA-Z0-9]/g, '') // unique per instance: several icons share a page
  const icon = AVATARS.find((a) => a.id === id)
  if (!icon) return null
  return (
    <svg className="forge-icon" viewBox="0 0 24 24" width="1em" height="1em" aria-hidden="true">
      <defs>
        <linearGradient id={g} x1="0" y1="0" x2="0.35" y2="1">
          <stop offset="0" style={{ stopColor: 'var(--accent-2)' }} />
          <stop offset="1" style={{ stopColor: 'var(--accent)' }} />
        </linearGradient>
      </defs>
      {icon.body && <path d={icon.body} fill={`url(#${g})`} fillRule="evenodd" />}
      {icon.strokes && <path d={icon.strokes} fill="none" stroke={`url(#${g})`} strokeWidth={2.6} strokeLinecap="round" strokeLinejoin="round" />}
      {icon.core && <path className="forge-icon-core" d={icon.core} />}
    </svg>
  )
}

// Avatar is a person's picked icon, or their initials, in a crucible: a ring of molten metal.
export function Avatar({ user, large }: { user: Pick<User, 'name' | 'email' | 'avatar'>; large?: boolean }) {
  return (
    <span className={large ? 'avatar large' : 'avatar'} aria-hidden="true">
      {AVATARS.some((a) => a.id === user.avatar) ? <Icon id={user.avatar} /> : <span className="avatar-initials">{initials(user)}</span>}
    </span>
  )
}
