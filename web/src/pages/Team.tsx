import { useEffect, useRef, useState, type FormEvent, type KeyboardEvent } from 'react'
import { Link, Navigate, useNavigate, useParams, useSearchParams } from 'react-router'
import { api, ApiError } from '../api'
import { useFetch } from '../useFetch'
import type { Member, People, Tag, TeamSummary, TeamView } from '../types'
import { ErrorBox } from '../components/ErrorBox'
import { Loader } from '../components/Loader'
import { useMe } from '../me'
import { Conflict, reportSaveError } from '../components/Conflict'
import { toast } from '../lib/alerts'
import { budgetRequest, membersOf, retag, rosterRequest, TAGS, tagPlural, teamPath } from '../lib/teamRequests'
import { peopleView, readQuery, writeQuery, type PeopleQuery } from '../lib/people'

const usd = (n: number) => `$${n.toFixed(2)}`
const STALE = 'Someone changed this team, reload to see the latest.'

export function TeamsIndex() {
  const { data, error } = useFetch<TeamSummary[]>('/api/teams')
  const { me } = useMe()
  const [starting, setStarting] = useState(false)
  if (error) return <ErrorBox error={error} />
  if (!data) return <Loader label="Gathering the smiths…" />
  if (data.length === 1 && !me.is_admin) return <Navigate to={`/teams/${data[0].id}`} replace />
  return (
    <section className="page">
      <h1>Teams</h1>
      <p className="lede">Each team has its leaders, seniors, juniors and trainees, and the trainings it owns.</p>
      {data.length === 0 && <p className="muted">You&apos;re not on a team yet.</p>}
      <div className="cards">
        {[...data].sort((a, b) => a.name.localeCompare(b.name)).map((t) => (
          <Link key={t.id} to={`/teams/${t.id}`} className="card team-card">
            <h2>{t.name}</h2>
            <p className="muted"><code>{t.id}</code> · <span className="badge">{t.role}</span></p>
          </Link>
        ))}
        {me.is_admin && !starting && (
          <button type="button" className="card new-card" onClick={() => setStarting(true)}><span aria-hidden="true">+</span> Start a team</button>
        )}
      </div>
      {starting && <CreateTeam onCancel={() => setStarting(false)} />}
    </section>
  )
}

// An admin starts a team here; the leader and the rest of the roster follow on the team's own page.
export function CreateTeam({ onCancel }: { onCancel?: () => void }) {
  const navigate = useNavigate()
  const [id, setId] = useState('')
  const [name, setName] = useState('')
  const [leader, setLeader] = useState('')
  const [busy, setBusy] = useState(false)
  const [status, setStatus] = useState('')
  const create = async (e: FormEvent) => {
    e.preventDefault()
    if (busy) return
    setBusy(true)
    setStatus('')
    try {
      await api(`/api/admin/teams/${encodeURIComponent(id.trim())}`, { method: 'POST', json: { name: name.trim(), members: { [leader.trim().toLowerCase()]: 'leader' } } })
      navigate(`/teams/${encodeURIComponent(id.trim())}`)
    } catch (err) {
      setStatus(err instanceof ApiError && err.status === 409 ? `A team called ${id.trim()} already exists.` : (err as Error).message)
    } finally {
      setBusy(false)
    }
  }
  return (
    <form className="stack panel" onSubmit={create}>
      <h2>Start a team</h2>
      <p className="muted">Name its first leader now; add the rest of the people on the team&apos;s own page. The id names its groups too, such as <code>{id.trim() || 'platform'}_seniors</code>.</p>
      <label>Team id (short, no spaces) <input required value={id} onChange={(e) => setId(e.target.value)} /></label>
      <label>Name <input required value={name} onChange={(e) => setName(e.target.value)} /></label>
      <label>Leader email <input required type="email" value={leader} onChange={(e) => setLeader(e.target.value)} /></label>
      <div className="row">
        <button className="primary" disabled={busy}>Create team</button>
        {onCancel && <button type="button" className="ghost" onClick={onCancel}>Cancel</button>}
      </div>
      <p role="status" className="fail">{status}</p>
    </form>
  )
}

export function TeamPage() {
  const { team } = useParams()
  const { data, error, reload } = useFetch<TeamView>(teamPath(team ?? ''))
  if (error) return <ErrorBox error={error} />
  if (!data) return <Loader label="Gathering the smiths…" />
  const leaders = data.members.filter((m) => m.tag === 'leader').map((m) => m.email)
  const trainees = data.members.filter((m) => m.tag === 'trainee').length
  const participants = data.programs.reduce((n, p) => n + p.resolved.participants.length, 0)
  return (
    <section className="page">
      <div className="row team-head">
        <div>
          <h1>{data.name}</h1>
          <p className="lede">{leaders.length > 0 ? `Led by ${leaders.join(', ')}` : 'This team has no leader; an admin names one below.'}</p>
        </div>
        <span className="spacer" />
        <Link className="button-link" to={`/teams/${data.id}/journey`}>Journey</Link>
      </div>
      <ul className="stats" aria-label="At a glance">
        <li><strong>{data.members.length}</strong> {data.members.length === 1 ? 'person' : 'people'}</li>
        <li><strong>{trainees}</strong> {trainees === 1 ? 'trainee' : 'trainees'}</li>
        <li><strong>{data.programs.length}</strong> {data.programs.length === 1 ? 'training' : 'trainings'}</li>
        <li><strong>{participants}</strong> taking them</li>
      </ul>
      <Roster key={`r-${data.version}`} team={data} onSaved={reload} />
      <Programs team={data} />
      <Budget key={`b-${data.budget?.version}`} team={data} onSaved={reload} />
      {data.is_admin && <RevokeAgents people={data.members.map((m) => m.email)} />}
      {data.is_admin && <DeleteTeam team={data} />}
    </section>
  )
}

// Roster lists the team's people with their tags: find someone, keep one tag, sort, or group by tag. A leader or an
// admin changes a tag by choosing it next to the person; an admin also adds people (moving them from another team) and
// takes them off.
function Roster({ team, onSaved }: { team: TeamView; onSaved: () => void }) {
  const people = useFetch<People>(team.is_admin ? '/api/org/people' : null)
  // The view is state, read from the URL once and written back after each change: React queues state updates, while
  // React Router's setSearchParams reads the URL as it is when called, so quick changes (typing) would drop each other.
  const [params, setParams] = useSearchParams()
  const [q, setView] = useState(() => readQuery(params))
  const setQ = (change: Partial<PeopleQuery>) => setView((v) => ({ ...v, ...change }))
  const url = new URLSearchParams(writeQuery(q)).toString()
  useEffect(() => { setParams(url, { replace: true }) }, [url, setParams])
  const [busy, setBusy] = useState(false)
  const [conflict, setConflict] = useState(false)
  const save: Save = async (members, ok) => {
    if (busy) return false
    setBusy(true)
    try {
      const r = rosterRequest(team, members)
      await api(r.path, { method: 'PUT', json: r.json })
      toast(ok)
      onSaved()
      return true
    } catch (err) {
      setConflict(reportSaveError(err))
      return false
    } finally {
      setBusy(false)
    }
  }
  const setTag = (m: Member, tag: Tag) => {
    if (tag === m.tag) return
    const r = retag(team, m.email, tag)
    if ('problem' in r) return toast(r.problem)
    void save(r.members, `${m.name || m.email} is now a ${tag}.`)
  }
  const remove = (m: Member) => {
    const members = membersOf(team)
    delete members[m.email]
    if (!Object.values(members).includes('leader')) return toast(`${team.name} needs a leader. Make someone else leader first.`)
    if (!window.confirm(`Take ${m.email} off ${team.name}? Their progress is kept.`)) return
    void save(members, `${m.email} is off ${team.name}.`)
  }
  const groups = peopleView(team.members, q)
  const shown = groups.reduce((n, g) => n + g.people.length, 0)
  const table = (rows: Member[], caption: string) => (
    <div className="table-wrap">
      <table className="grid people">
        <caption className="sr-only">{caption}</caption>
        <thead><tr><th scope="col">Person</th><th scope="col">Tag</th>{team.is_admin && <th scope="col"><span className="sr-only">Actions</span></th>}</tr></thead>
        <tbody>
          {rows.map((m) => (
            <tr key={m.email}>
              <td>{m.name ? <><strong>{m.name}</strong> <span className="muted">{m.email}</span></> : m.email}</td>
              <td><TagPicker member={m} editable={team.can_edit_team} busy={busy} onPick={(t) => setTag(m, t)} /></td>
              {team.is_admin && (
                <td className="actions">
                  <button type="button" className="ghost small" aria-label={`Take ${m.email} off the team`} disabled={busy} onClick={() => remove(m)}>Remove</button>
                </td>
              )}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
  return (
    <section className="panel" aria-labelledby="people">
      <div className="row panel-head">
        <h2 id="people">People</h2>
        <span className="spacer" />
        <span className="muted">{shown === team.members.length ? `${shown} ${shown === 1 ? 'person' : 'people'}` : `${shown} of ${team.members.length}`}</span>
      </div>
      {conflict && <Conflict onReload={onSaved} message={STALE} />}
      {team.can_edit_team && <p className="muted">Choose someone&apos;s tag to change it.</p>}
      <div className="toolbar people-tools">
        <label>Find <input type="search" value={q.q} placeholder="a name or email" onChange={(e) => setQ({ q: e.target.value })} /></label>
        <label>Tag
          <select value={q.tag} onChange={(e) => setQ({ tag: e.target.value as Tag | '' })}>
            <option value="">Every tag</option>
            {TAGS.map((t) => <option key={t} value={t}>{tagPlural[t]}</option>)}
          </select>
        </label>
        <label>Sort by
          <select value={q.sort} onChange={(e) => setQ({ sort: e.target.value as PeopleQuery['sort'] })}>
            <option value="tag">Tag</option>
            <option value="name">Name</option>
          </select>
        </label>
        <div className="segmented" role="group" aria-label="Show people">
          <button type="button" aria-pressed={!q.group} onClick={() => setQ({ group: false })}>List</button>
          <button type="button" aria-pressed={q.group} onClick={() => setQ({ group: true })}>By tag</button>
        </div>
      </div>
      {shown === 0 && <p className="muted empty">{team.members.length === 0 ? 'Nobody is on this team yet.' : 'Nobody matches. Clear the search or pick every tag.'}</p>}
      {q.group
        ? groups.map((g) => (
            <section key={g.tag} className="tag-group" aria-label={tagPlural[g.tag as Tag]}>
              <h3>{tagPlural[g.tag as Tag]} <span className="muted">{g.people.length}</span></h3>
              {table(g.people, tagPlural[g.tag as Tag])}
            </section>
          ))
        : shown > 0 && table(groups[0].people, `People on ${team.name}`)}
      {team.is_admin && <AddPerson team={team} people={people.data} busy={busy} save={save} />}
    </section>
  )
}

type Save = (members: Record<string, Tag>, ok: string) => Promise<boolean>

// TagPicker is a person's tag. For someone who may change it, it is a button that opens a menu of the four tags
// (as UserMenu does: Escape or a click outside closes it, focus returns to the button, the arrow keys move between tags).
function TagPicker({ member, editable, busy, onPick }: { member: Member; editable: boolean; busy: boolean; onPick: (t: Tag) => void }) {
  const [open, setOpen] = useState(false)
  const box = useRef<HTMLSpanElement>(null)
  const button = useRef<HTMLButtonElement>(null)
  const items = useRef<(HTMLButtonElement | null)[]>([])
  useEffect(() => {
    if (!open) return
    items.current[TAGS.indexOf(member.tag)]?.focus()
    const onDown = (e: MouseEvent) => { if (!box.current?.contains(e.target as Node)) setOpen(false) }
    document.addEventListener('mousedown', onDown)
    return () => document.removeEventListener('mousedown', onDown)
  }, [open, member.tag])
  if (!editable) return <span className={`tag-pill tag-${member.tag}`}>{member.tag}</span>
  const close = () => { setOpen(false); button.current?.focus() }
  const onKey = (e: KeyboardEvent) => {
    if (e.key === 'Escape' && open) { e.stopPropagation(); close() }
    if (open && (e.key === 'ArrowDown' || e.key === 'ArrowUp')) {
      e.preventDefault()
      const at = items.current.findIndex((el) => el === document.activeElement)
      items.current[(at + (e.key === 'ArrowDown' ? 1 : TAGS.length - 1)) % TAGS.length]?.focus()
    }
  }
  return (
    <span className="tag-picker" ref={box} onKeyDown={onKey}>
      <button ref={button} type="button" className={`tag-pill tag-${member.tag}`} aria-haspopup="menu" aria-expanded={open} disabled={busy}
        aria-label={`${member.name || member.email} is a ${member.tag}. Change their tag`} onClick={() => setOpen(!open)}>
        {member.tag} <span aria-hidden="true">▾</span>
      </button>
      {open && (
        <span className="tag-menu" role="menu" aria-label={`Tag for ${member.name || member.email}`}>
          {TAGS.map((t, i) => (
            <button key={t} ref={(el) => { items.current[i] = el }} type="button" role="menuitemradio" aria-checked={t === member.tag}
              className={`tag-pill tag-${t}`} onClick={() => { close(); onPick(t) }}>{t}</button>
          ))}
        </span>
      )}
    </span>
  )
}

// AddPerson (admins): anyone Crucible knows, or a new email. Someone on another team moves here.
function AddPerson({ team, people, busy, save }: { team: TeamView; people?: People; busy: boolean; save: Save }) {
  const [email, setEmail] = useState('')
  const [tag, setTag] = useState<Tag>('trainee')
  const [problem, setProblem] = useState('')
  const listId = `people-${team.id}`
  const members = membersOf(team)
  const candidates = (people?.people ?? []).filter((p) => !members[p.email])
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    const who = email.trim().toLowerCase()
    if (!/^[^\s@]+@[^\s@]+$/.test(who)) return setProblem('Enter an email address, such as someone@example.com.')
    if (members[who]) return setProblem(`${who} is already on ${team.name}; change their tag in the list.`)
    const from = people?.people.find((p) => p.email === who)?.team
    if (from && !window.confirm(`${who} is on ${from}. Move them to ${team.name} as a ${tag}? They leave ${from}.`)) return
    setProblem('')
    if (await save({ ...members, [who]: tag }, from ? `${who} moved to ${team.name}.` : `${who} joined ${team.name}.`)) setEmail('')
  }
  return (
    <form className="row retag" onSubmit={submit}>
      <span className="role-name">Add someone</span>
      <input aria-label="Person to add" list={listId} value={email} placeholder="an email" onChange={(e) => setEmail(e.target.value)} />
      <datalist id={listId}>{candidates.map((p) => <option key={p.email} value={p.email}>{p.team ? `${p.name || p.email} · ${p.tag} on ${p.team}` : p.name || 'on no team'}</option>)}</datalist>
      <select aria-label="Their tag" value={tag} onChange={(e) => setTag(e.target.value as Tag)}>
        {TAGS.map((t) => <option key={t} value={t}>{t}</option>)}
      </select>
      <button className="primary" disabled={busy || !email.trim()}>Add to team</button>
      {problem && <p role="alert" className="error">{problem}</p>}
    </form>
  )
}

// Offboarding: an admin disconnects a person's laptop agent and revokes its pairing token (audited).
function RevokeAgents({ people }: { people: string[] }) {
  const emails = [...new Set(people.filter(Boolean))]
  return (
    <section className="panel" aria-labelledby="agents">
      <h2 id="agents">Laptop agents</h2>
      <p className="muted">Revoke a person&apos;s pairing token when they leave. Their agent is disconnected at once; labs already running end by idle or TTL.</p>
      <div className="table-wrap"><table className="grid">
        <tbody>
          {emails.map((email) => (
            <tr key={email}>
              <td>{email}</td>
              <td className="actions">
                <button className="ghost small" onClick={async () => {
                  if (!window.confirm(`Revoke the pairing token of ${email}?`)) return
                  try {
                    await api(`/api/admin/agent/tokens?email=${encodeURIComponent(email)}`, { method: 'DELETE' })
                    toast(`Pairing revoked for ${email}`)
                  } catch (err) {
                    reportSaveError(err)
                  }
                }}>Revoke agent</button>
              </td>
            </tr>
          ))}
        </tbody>
      </table></div>
    </section>
  )
}

// DeleteTeam removes the team and its people's tags. A team that still owns trainings is refused by the server.
function DeleteTeam({ team }: { team: TeamView }) {
  const navigate = useNavigate()
  const [busy, setBusy] = useState(false)
  const remove = async () => {
    if (busy || !window.confirm(`Delete ${team.name}? Its people leave it and lose their tags. Everyone's progress is kept.`)) return
    setBusy(true)
    try {
      await api(`/api/admin/teams/${encodeURIComponent(team.id)}`, { method: 'DELETE' })
      toast(`${team.name} is deleted.`)
      navigate('/teams')
    } catch (err) {
      reportSaveError(err)
      setBusy(false)
    }
  }
  return (
    <section className="panel danger-zone" aria-labelledby="delete-team">
      <h2 id="delete-team">Delete this team</h2>
      <p className="muted">Removes the team, its tags and budget. Give its trainings to another team or delete them first. Progress, scores and badges are kept.</p>
      <button type="button" className="ghost danger-text" disabled={busy} onClick={remove}>Delete team</button>
    </section>
  )
}

// Programs lists the trainings the team owns. Registering one, its roles and its settings live on the trainings page.
function Programs({ team }: { team: TeamView }) {
  const { me } = useMe()
  return (
    <section className="panel" aria-labelledby="programs">
      <div className="row panel-head">
        <h2 id="programs">Trainings it owns</h2>
        <span className="spacer" />
        {me.can_manage_trainings && <Link to="/trainings/manage?new">Register a training</Link>}
      </div>
      {team.programs.length === 0 && <p className="muted">This team owns no training yet. Its leaders, seniors and juniors can register one.</p>}
      {team.programs.length > 0 && (
        <div className="table-wrap"><table className="grid">
          <thead><tr><th>Training</th><th>Taking it</th><th>Schedule</th><th>Budget</th><th /></tr></thead>
          <tbody>
            {team.programs.map((p) => (
              <tr key={p.training}>
                <td>{p.title}</td>
                <td>{p.resolved.participants.length}</td>
                <td>{p.schedule || 'any time'}</td>
                <td>{p.budget_usd_month ? `${usd(p.budget_usd_month)}/month` : '—'}</td>
                <td className="actions">{me.can_manage_trainings && <Link to={`/trainings/manage/${p.training}`}>{p.can_manage ? 'Manage' : 'Details'}</Link>}</td>
              </tr>
            ))}
          </tbody>
        </table></div>
      )}
    </section>
  )
}

function Budget({ team, onSaved }: { team: TeamView; onSaved: () => void }) {
  const [monthly, setMonthly] = useState(String(team.budget?.monthly_usd ?? 0))
  const [cap, setCap] = useState(String(team.budget?.hard_cap_usd ?? 0))
  const [conflict, setConflict] = useState(false)
  const [busy, setBusy] = useState(false)
  // The server only sends the budget to people who may see it.
  if (!team.budget) return null
  const summary = team.budget.monthly_usd
    ? `${usd(team.budget.monthly_usd)} a month · hard cap ${usd(team.budget.hard_cap_usd)}`
    : 'No team budget set.'
  if (!team.is_admin) return (<section className="panel" aria-labelledby="budget"><h2 id="budget">Budget</h2><p>{summary}</p></section>)
  const save = async (e: FormEvent) => {
    e.preventDefault()
    if (busy) return
    setBusy(true)
    try {
      const r = budgetRequest(team, monthly, cap)
      await api(r.path, { method: 'PUT', json: r.json })
      toast('Budget saved')
      onSaved()
    } catch (err) {
      setConflict(reportSaveError(err))
    } finally {
      setBusy(false)
    }
  }
  return (
    <form className="stack panel" onSubmit={save} aria-labelledby="budget">
      <h2 id="budget">Budget</h2>
      <p className="muted">{summary} An alert goes out at 80%; requests that would pass the cap need an admin.</p>
      {conflict && <Conflict onReload={onSaved} message="Someone changed this budget, reload to see the latest." />}
      <label>Monthly budget (USD) <input type="number" min={0} step="0.01" value={monthly} onChange={(e) => setMonthly(e.target.value)} /></label>
      <label>Hard cap (USD, empty = the budget) <input type="number" min={0} step="0.01" value={cap} onChange={(e) => setCap(e.target.value)} /></label>
      <button className="primary" disabled={busy}>Save budget</button>
    </form>
  )
}
