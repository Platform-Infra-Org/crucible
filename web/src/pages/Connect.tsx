import { useState } from 'react'
import { api } from '../api'
import { useFetch } from '../useFetch'

export function ConnectPage() {
  const [cmd, setCmd] = useState<{ token: string; command: string }>()
  const { data: status } = useFetch<{ online: boolean }>('/api/agent/status', 3000)
  return (
    <section className="page">
      <h1>Connect your laptop</h1>
      <p>
        Local labs run in Docker on your own machine through <code>crucible-agent</code>. You need Docker with the compose plugin (macOS, Linux, or Windows with WSL2).
      </p>
      <p className={status?.online ? 'pass' : 'muted'} role="status">
        {status?.online ? '● Agent connected. Your forge is lit.' : '○ No agent connected yet.'}
      </p>
      <ol>
        <li>Get <code>crucible-agent</code> for your platform from your admin (or build it with <code>make build</code>).</li>
        <li>Generate a pairing token. A new token revokes the previous one.</li>
        <li>Run the command below and leave it running while you do labs.</li>
      </ol>
      <button className="primary" onClick={async () => setCmd(await api('/api/agent/tokens', { method: 'POST' }))}>Generate pairing token</button>
      {cmd && (
        <>
          <pre className="command">{cmd.command}</pre>
          <p className="muted">
            Token (shown once): <code data-testid="pairing-token">{cmd.token}</code>
          </p>
          <button className="ghost" onClick={() => navigator.clipboard.writeText(cmd.command)}>Copy command</button>
        </>
      )}
    </section>
  )
}
