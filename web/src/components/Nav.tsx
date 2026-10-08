import { Link, NavLink } from 'react-router'
import { useMe } from '../App'
import { AdminMenu } from './AdminMenu'
import { HelpLink } from './HelpLink'
import { UserMenu } from './UserMenu'

export function Nav() {
  const { me } = useMe()
  return (
    <nav className="nav" aria-label="Main">
      <Link to="/" className="brand">
        <span className="brand-mark">⚒</span> Crucible
      </Link>
      <NavLink to="/" end>Hearth</NavLink>
      <NavLink to="/trainings">Trainings</NavLink>
      <NavLink to="/labs">Labs</NavLink>
      {me.can_score && <NavLink to="/anvil">Anvil</NavLink>}
      {me.can_view_spend && <NavLink to="/ledger">Ledger</NavLink>}
      {(me.teams.length > 0 || me.is_admin) && <NavLink to="/teams">Teams</NavLink>}
      {me.can_approve && <NavLink to="/approvals">Approvals</NavLink>}
      {me.is_mentor && <NavLink to="/mentor">Mentor</NavLink>}
      <NavLink to="/connect">Connect your laptop</NavLink>
      <NavLink to="/docs">Docs</NavLink>
      <NavLink to="/settings">Settings</NavLink>
      <span className="spacer" />
      {me.is_admin && <AdminMenu />}
      <HelpLink />
      <UserMenu />
    </nav>
  )
}
