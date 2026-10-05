import { Link, NavLink } from 'react-router'
import { useMe } from '../App'

export function Nav() {
  const { me } = useMe()
  const logout = async () => {
    await fetch('/auth/logout', { method: 'POST' })
    window.location.href = '/auth/login'
  }
  return (
    <nav className="nav" aria-label="Main">
      <Link to="/" className="brand">
        <span className="brand-mark">⚒</span> Crucible
      </Link>
      <NavLink to="/" end>Hearth</NavLink>
      {(me.teams.length > 0 || me.is_admin) && <NavLink to="/teams">Team</NavLink>}
      <NavLink to="/connect">Connect your laptop</NavLink>
      <NavLink to="/settings">Settings</NavLink>
      <span className="spacer" />
      <span className="muted">{me.user.name || me.user.email}</span>
      <button className="ghost" onClick={logout}>Log out</button>
    </nav>
  )
}
