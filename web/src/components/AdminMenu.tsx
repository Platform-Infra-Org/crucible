import { useEffect, useRef, useState } from 'react'
import { NavLink, useLocation } from 'react-router'

const items = [
  { to: '/admin', label: 'Forge Status', end: true },
  { to: '/admin/settings', label: 'Forge settings', end: false },
  { to: '/admin/trainings', label: 'Registry', end: false },
]

// AdminMenu gathers the admin-only pages behind one control, so the main row stays the things
// everyone uses. Rendered only for admins.
export function AdminMenu() {
  const [open, setOpen] = useState(false)
  const box = useRef<HTMLDivElement>(null)
  const button = useRef<HTMLButtonElement>(null)
  const here = useLocation().pathname.startsWith('/admin')

  useEffect(() => {
    if (!open) return
    const onDown = (e: MouseEvent) => {
      if (!box.current?.contains(e.target as Node)) setOpen(false)
    }
    document.addEventListener('mousedown', onDown)
    return () => document.removeEventListener('mousedown', onDown)
  }, [open])

  const close = (toButton: boolean) => {
    setOpen(false)
    if (toButton) button.current?.focus()
  }

  // Arrow keys walk the menu; Escape closes it and hands focus back, so the keyboard never strands.
  const onKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Escape') {
      e.stopPropagation()
      close(true)
      return
    }
    if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return
    e.preventDefault()
    const links = Array.from(box.current?.querySelectorAll<HTMLAnchorElement>('[role="menuitem"]') ?? [])
    if (links.length === 0) return
    const at = links.indexOf(document.activeElement as HTMLAnchorElement)
    const next = e.key === 'ArrowDown' ? at + 1 : at - 1
    links[(next + links.length) % links.length].focus()
  }

  return (
    <div className="admin-menu" ref={box} onKeyDown={onKeyDown}>
      <button
        ref={button}
        type="button"
        className={here ? 'admin-menu-button active' : 'admin-menu-button'}
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setOpen(!open)}
      >
        Administrator <span aria-hidden="true">▾</span>
      </button>
      {open && (
        <div className="admin-menu-list" role="menu" aria-label="Administrator">
          {items.map((i) => (
            <NavLink key={i.to} to={i.to} end={i.end} role="menuitem" onClick={() => close(false)}>
              {i.label}
            </NavLink>
          ))}
        </div>
      )}
    </div>
  )
}
