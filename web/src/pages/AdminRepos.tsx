import { useState, type FormEvent } from 'react'
import { api, ApiError } from '../api'
import { useFetch } from '../useFetch'
import type { ReposView, Repo } from '../types'
import { ErrorBox } from '../components/ErrorBox'
import { Loader } from '../components/Loader'
import { toast } from '../lib/alerts'

// A repository is connected once and holds any number of trainings, so its credential is entered once and rotated
// once. The page never shows a credential: it says whether one is set, and lets you replace it.
export function AdminReposPage() {
  const { data, error, reload } = useFetch<ReposView>('/api/admin/repos')
  if (error) return <ErrorBox error={error} />
  if (!data) return <Loader label="Counting the forge's sources…" />
  return (
    <section className="page">
      <h1>Repositories</h1>
      <p className="lede">Where training content comes from. Connect a repository once; every training that reads
        from it shares the connection, so a credential is entered once and rotated in one place.</p>
      {!data.can_keep_secrets && (
        <p role="alert" className="warn">This instance has no <code>CRUCIBLE_SECRET_KEY</code>, so it cannot keep a
          credential. Public repositories work; a private one needs that key set first.</p>
      )}
      <ul className="cards repos">
        {data.repos.map((r) => <RepoCard key={r.id} repo={r} canKeepSecrets={data.can_keep_secrets} onChange={reload} />)}
      </ul>
      {data.repos.length === 0 && <p className="muted empty">No repositories yet. Connect one below, then register a training from it.</p>}
      <Connect canKeepSecrets={data.can_keep_secrets} onChange={reload} />
    </section>
  )
}

function statusOf(r: Repo): { text: string; cls: string } {
  if (r.status === 'ok') return { text: 'Reachable', cls: 'badge complete' }
  if (r.status === 'failed') return { text: 'Cannot be read', cls: 'badge warn' }
  return { text: 'Not checked yet', cls: 'badge' }
}

function RepoCard({ repo, canKeepSecrets, onChange }: { repo: Repo; canKeepSecrets: boolean; onChange: () => void }) {
  const [busy, setBusy] = useState(false)
  const [open, setOpen] = useState(false)
  const s = statusOf(repo)
  // What a URL can authenticate with: a token over http(s), a deploy key over ssh, nothing for a local path.
  const takesToken = /^https?:\/\//i.test(repo.url)
  const takesKey = isSSH(repo.url)
  const act = async (fn: () => Promise<unknown>, ok: string) => {
    if (busy) return
    setBusy(true)
    try {
      await fn()
      toast(ok)
      onChange()
    } catch (e) {
      toast(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <li className="card repo">
      <div className="row repo-head">
        <h2><code>{repo.url}</code></h2>
        <span className="spacer" />
        <span className={s.cls}>{s.text}</span>
        <span className="badge">{repo.auth === 'token' ? 'Token' : repo.auth === 'ssh' ? 'Deploy key' : 'No credential'}</span>
      </div>
      {repo.last_error && <p className="error">{repo.last_error}</p>}
      <p className="repo-trainings">
        <span className="eyebrow">Trainings</span>
        {repo.trainings.length > 0
          ? <span className="chips">{repo.trainings.map((t) => <span key={t} className="chip">{t}</span>)}</span>
          : <span className="muted">none yet</span>}
      </p>
      {repo.checked_at && <p className="hint">Last checked {new Date(repo.checked_at).toLocaleString()}</p>}
      <div className="row repo-actions">
        <button type="button" className="ghost small" disabled={busy}
          onClick={() => act(() => api(`/api/admin/repos/${repo.id}/check`, { method: 'POST', json: {} }), `Checked ${repo.url}.`)}>Test connection</button>
        {(takesToken || takesKey) && (
          <button type="button" className="ghost small" onClick={() => setOpen(!open)} aria-expanded={open}>
            {repo.has_secret ? 'Replace credential' : 'Add a credential'}
          </button>
        )}
        <span className="spacer" />
        <button type="button" className="ghost small danger-text" disabled={busy}
          onClick={() => window.confirm(`Disconnect ${repo.url}? Crucible stops reading from it. The repository itself is not touched.`)
            && act(() => api(`/api/admin/repos/${repo.id}`, { method: 'DELETE' }), `${repo.url} is disconnected.`)}>Disconnect</button>
      </div>
      {repo.auth === 'ssh' && repo.public_key && <DeployKey repo={repo} />}
      {open && <Credential repo={repo} canKeepSecrets={canKeepSecrets} onDone={() => { setOpen(false); onChange() }} />}
    </li>
  )
}

// Credential replaces what a connected repository authenticates with. Leaving the token empty keeps the stored one,
// so saving a change of username cannot blank a credential this page never saw.
function Credential({ repo, canKeepSecrets, onDone }: { repo: Repo; canKeepSecrets: boolean; onDone: () => void }) {
  const [auth, setAuth] = useState(repo.auth)
  const [username, setUsername] = useState(repo.username)
  const [secret, setSecret] = useState('')
  const [busy, setBusy] = useState(false)
  const save = async (e: FormEvent) => {
    e.preventDefault()
    if (busy) return
    setBusy(true)
    try {
      const body = { url: repo.url, auth, username, secret: auth === 'ssh' ? 'new' : secret, version: repo.version }
      await api(`/api/admin/repos/${repo.id}`, { method: 'PUT', json: body })
      toast(auth === 'none' ? 'Credential removed.' : 'Credential saved.')
      onDone()
    } catch (e) {
      toast(e instanceof ApiError && e.status === 409 ? 'Someone else changed this repository; reload and try again.' : (e as Error).message)
    } finally {
      setBusy(false)
    }
  }
  return (
    <form className="stack" onSubmit={save}>
      <Kinds url={repo.url} auth={auth} setAuth={setAuth} canKeepSecrets={canKeepSecrets} />
      {auth === 'token' && <TokenFields username={username} setUsername={setUsername} secret={secret} setSecret={setSecret} hasSecret={repo.has_secret} />}
      {auth === 'ssh' && <p className="muted">Crucible makes the keypair and keeps the private half; you add the
        public half to the repository. {repo.auth === 'ssh' && 'Saving again makes a new pair, so the old key stops working.'}</p>}
      <button className="primary" disabled={busy}>Save credential</button>
    </form>
  )
}

// isSSH matches what the server will accept as an SSH repository (org.IsSSH).
export function isSSH(url: string) {
  return /^ssh:\/\//i.test(url) || (!url.includes('://') && /^[^@/:\s]+@/.test(url))
}

function Kinds({ url, auth, setAuth, canKeepSecrets }: { url: string; auth: Repo['auth']; setAuth: (a: Repo['auth']) => void; canKeepSecrets: boolean }) {
  return (
    <label>How Crucible signs in
      <select value={auth} onChange={(e) => setAuth(e.target.value as Repo['auth'])}>
        <option value="none">Nothing — the repository is public</option>
        <option value="token" disabled={!canKeepSecrets || !/^https?:\/\//i.test(url)}>An access token</option>
        <option value="ssh" disabled={!canKeepSecrets || !isSSH(url)}>A deploy key Crucible makes</option>
      </select>
    </label>
  )
}

// DeployKey shows the public half, which is not a secret: it is what goes into the repository as a deploy key, and
// it can be shown again at any time — unlike a token, which a host shows once.
function DeployKey({ repo }: { repo: Repo }) {
  return (
    <div className="deploy-key">
      <p className="muted">Add this to <code>{repo.url}</code> as a deploy key <strong>with write access</strong>, then
        choose Test connection. On GitHub: Settings → Deploy keys → Add deploy key, and tick <em>Allow write
        access</em>. On GitLab: Settings → Repository → Deploy keys, with <em>Grant write permissions</em>.</p>
      <pre className="key"><code>{repo.public_key}</code></pre>
      <button type="button" className="ghost small" onClick={() => { void navigator.clipboard?.writeText(repo.public_key); toast('Public key copied.') }}>Copy the key</button>
    </div>
  )
}

function TokenFields({ username, setUsername, secret, setSecret, hasSecret }: {
  username: string; setUsername: (v: string) => void; secret: string; setSecret: (v: string) => void; hasSecret: boolean
}) {
  return (
    <>
      <p className="muted">Give Crucible a token that may <strong>read and write</strong> this repository&apos;s
        contents — write, because merging an approved edit pushes to it. On GitHub: Settings → Developer settings →
        Fine-grained tokens, limited to this one repository, Contents: Read and write; an organisation may have to
        approve it. On GitLab: a project access token with <code>write_repository</code>.</p>
      <label>User name (optional) <input value={username} placeholder="GitHub ignores it; GitLab wants any name"
        onChange={(e) => setUsername(e.target.value)} /></label>
      <label>Token <input type="password" autoComplete="off" value={secret} placeholder={hasSecret ? 'Set — type a new one to replace it' : ''}
        onChange={(e) => setSecret(e.target.value)} /></label>
      <p className="hint">Crucible stores it encrypted and never shows it again, here or anywhere else.</p>
    </>
  )
}

function Connect({ canKeepSecrets, onChange }: { canKeepSecrets: boolean; onChange: () => void }) {
  const [url, setUrl] = useState('')
  const [auth, setAuth] = useState<Repo['auth']>('none')
  const [username, setUsername] = useState('')
  const [secret, setSecret] = useState('')
  const [busy, setBusy] = useState(false)
  const save = async (e: FormEvent) => {
    e.preventDefault()
    if (busy) return
    setBusy(true)
    try {
      await api('/api/admin/repos', { method: 'POST', json: { url: url.trim(), auth, username, secret } })
      toast(`${url.trim()} is connected.`)
      setUrl(''); setSecret(''); setUsername(''); setAuth('none')
      onChange()
    } catch (e) {
      toast(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <form className="stack panel" onSubmit={save}>
      <h2>Connect a repository</h2>
      <label>Repository URL <input required value={url} placeholder="https://github.com/your-org/trainings.git"
        onChange={(e) => setUrl(e.target.value)} /></label>
      <Kinds url={url} auth={auth} setAuth={setAuth} canKeepSecrets={canKeepSecrets} />
      {auth === 'token' && <TokenFields username={username} setUsername={setUsername} secret={secret} setSecret={setSecret} hasSecret={false} />}
      {auth === 'ssh' && <p className="muted">Crucible makes a keypair for this repository and shows the public half
        once it is connected; you add that to the repository as a deploy key with write access.</p>}
      <button className="primary" disabled={busy}>Connect repository</button>
    </form>
  )
}
