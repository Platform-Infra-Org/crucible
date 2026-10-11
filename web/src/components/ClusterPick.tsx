import type { KubeContext } from '../types'

// ClusterPick: which context of the trainee's kubeconfig the lab runs on, and consent for a cluster-wide lab.
export function ClusterPick({ contexts, value, onChange, clusterWide, consent, onConsent }: {
  contexts: KubeContext[]; value: string; onChange: (name: string) => void; clusterWide: boolean; consent: boolean; onConsent: (ok: boolean) => void
}) {
  if (contexts.length === 0) {
    return (
      <p className="muted cluster-pick">
        Your laptop agent doesn&apos;t see a cluster. No cluster? Run <code>crucible-agent cluster up</code> to make one on this laptop (needs Docker), or point the agent at
        your kubeconfig with <code>--kubeconfig</code>, and it shows up here.
      </p>
    )
  }
  return (
    <div className="stack cluster-pick">
      <label>Run it on
        <select value={value} onChange={(e) => onChange(e.target.value)}>
          <option value="">Pick a cluster…</option>
          {contexts.map((c) => <option key={c.name} value={c.name}>{c.name} · {c.server || 'no server'}</option>)}
        </select>
      </label>
      <p className="muted">The lab installs into its own namespace there and is removed when it ends.</p>
      {clusterWide && (
        <label className="check">
          <input type="checkbox" checked={consent} onChange={(e) => onConsent(e.target.checked)} />
          This lab creates cluster-wide resources (like CRDs) that may stay on the cluster after it ends.
        </label>
      )}
    </div>
  )
}
