import { useMe } from '../App'
import { THEMES } from '../theme/theme'

export function SettingsPage() {
  const { me, setPrefs } = useMe()
  const theme = me.user.theme || me.default_theme
  return (
    <section className="page">
      <h1>Settings</h1>
      <h2>Theme</h2>
      <div className="themes" role="radiogroup" aria-label="Theme">
        {THEMES.map((t) => (
          <button key={t.id} role="radio" aria-checked={theme === t.id} onClick={() => setPrefs(t.id, me.user.calm_motion)}>
            <span className="swatch-preview" data-theme={t.id} />
            <strong>{t.name}</strong>
            <small className="muted">{t.hint}</small>
          </button>
        ))}
      </div>
      <h2>Motion</h2>
      <label>
        <input type="checkbox" checked={me.user.calm_motion} onChange={(e) => setPrefs(theme, e.target.checked)} /> Calm forge (turn off animations)
      </label>
    </section>
  )
}
