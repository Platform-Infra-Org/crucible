import { useState } from 'react'
import { useMe } from '../App'
import { api } from '../api'
import { useFetch } from '../useFetch'
import { toast } from '../lib/alerts'
import type { NotificationPrefs } from '../types'
import { THEMES } from '../theme/theme'

export function SettingsPage() {
  const { me, setPrefs } = useMe()
  const theme = me.user.theme || me.default_theme
  return (
    <section className="page">
      <h1>Settings</h1>
      <section className="panel" aria-labelledby="theme-h">
      <h2 id="theme-h">Theme</h2>
      <div className="themes" role="radiogroup" aria-label="Theme">
        {THEMES.map((t) => (
          <button key={t.id} role="radio" aria-checked={theme === t.id} onClick={() => setPrefs(t.id, me.user.calm_motion)}>
            <span className="swatch-preview" data-theme={t.id} />
            <strong>{t.name}</strong>
            <small className="muted">{t.hint}</small>
          </button>
        ))}
      </div>
      </section>
      <section className="panel" aria-labelledby="motion-h">
      <h2 id="motion-h">Motion</h2>
      <label>
        <input type="checkbox" checked={me.user.calm_motion} onChange={(e) => setPrefs(theme, e.target.checked)} /> Calm forge (turn off animations)
      </label>
      </section>
      <NotificationSettings />
    </section>
  )
}

function NotificationSettings() {
  const { data, error, reload } = useFetch<NotificationPrefs>('/api/me/notifications')
  const [saving, setSaving] = useState(false)
  if (error) return <p className="error">{error.message}</p>
  if (!data) return null
  const toggle = async (kind: string, muted: boolean) => {
    const next = data.kinds.filter((k) => (k.kind === kind ? muted : k.muted)).map((k) => k.kind)
    setSaving(true)
    try {
      await api('/api/me/notifications', { method: 'PUT', json: { muted: next } })
      reload()
    } catch (e) {
      toast((e as Error).message)
    } finally {
      setSaving(false)
    }
  }
  return (
    <section className="panel" aria-labelledby="email-h">
      <h2 id="email-h">Email notifications</h2>
      {!data.email_enabled && <p className="muted">Email is not configured on this Crucible yet; these choices apply once it is.</p>}
      <fieldset className="stack" disabled={saving}>
        <legend className="muted">Email me when…</legend>
        {data.kinds.map((k) => (
          <label key={k.kind}>
            <input type="checkbox" checked={!k.muted} onChange={(e) => toggle(k.kind, !e.target.checked)} /> {k.label}
          </label>
        ))}
      </fieldset>
    </section>
  )
}
