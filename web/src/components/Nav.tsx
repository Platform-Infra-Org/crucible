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
      {me.can_approve && <NavLink to="/approvals">Approvals</NavLink>}
      {me.is_mentor && <NavLink to="/mentor">Mentor</NavLink>}
      {me.can_view_spend && <NavLink to="/ledger">Ledger</NavLink>}
      {me.can_score && <NavLink to="/anvil">Anvil</NavLink>}
      {me.is_admin && <NavLink to="/admin">Forge Status</NavLink>}
      <NavLink to="/connect">Connect your laptop</NavLink>
      <NavLink to="/settings">Settings</NavLink>
      <span className="spacer" />
      <span className="muted">{me.user.name || me.user.email}</span>
      <button className="ghost" onClick={logout}>Log out</button>
    </nav>
  )
}
