import { useState } from 'react'
import { Embers } from './Embers'
import { Forging } from './Forging'
import { RuneSword } from './RuneSword'

const LOOKS = [{ id: 'anvil', label: 'Anvil' }, { id: 'sword', label: 'Rune sword' }, { id: 'none', label: 'None' }] as const
type Look = (typeof LOOKS)[number]['id']
const KEY = 'crucible-gate-animation'

// The gate's animation, remembered in this browser only; the anvil when nothing (or nothing readable) is stored.
function storedLook(): Look {
  try { return LOOKS.find((l) => l.id === localStorage.getItem(KEY))?.id ?? 'anvil' } catch { return 'anvil' }
}

// ForgeGate is what someone who isn't signed in sees: the forge at work, and the way in. Signing in itself happens at
// the company's identity provider (Keycloak or Cognito); Enter the forge goes there and comes back to the Hearth.
// Two animations tell the ranks, the anvil's hammer or the rune sword; None shows neither and puts the title at the
// centre of the screen, the embers still drifting behind it. The switch at the bottom picks one, for now.
export function ForgeGate({ calm }: { calm: boolean }) {
  const [going, setGoing] = useState(false)
  const [look, setLook] = useState<Look>(storedLook)
  const choose = (l: Look) => {
    setLook(l)
    try { localStorage.setItem(KEY, l) } catch { /* the choice just won't be remembered */ }
  }
  return (
    <main className={look === 'none' ? 'gate plain' : 'gate'}>
      {!calm && <Embers count={24} />}
      <div className="gate-inner">
        <div className="gate-title">
          <p className="gate-brand"><span className="brand-mark" aria-hidden="true">⚒</span> Crucible</p>
          <p className="gate-motto">The crucible doesn't break the metal. It reveals it.</p>
        </div>
        {look === 'sword' ? <RuneSword calm={calm} /> : look === 'anvil' && <Forging calm={calm} />}
        <div className="gate-way">
          <button type="button" className="primary big" disabled={going} onClick={() => { setGoing(true); window.location.assign('/auth/login') }}>
            {going ? 'Opening the gate…' : 'Enter the forge'}
          </button>
          <p className="gate-note">You sign in with your company account.</p>
          <div className="segmented gate-switch" role="group" aria-label="Animation">
            {LOOKS.map((l) => (
              <button key={l.id} type="button" aria-pressed={look === l.id} onClick={() => choose(l.id)}>{l.label}</button>
            ))}
          </div>
        </div>
      </div>
    </main>
  )
}
