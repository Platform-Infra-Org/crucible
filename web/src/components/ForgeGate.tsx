import { useState } from 'react'
import { Embers } from './Embers'
import { Forging } from './Forging'

// ForgeGate is what someone who isn't signed in sees: the forge at work, and the way in. Signing in itself happens at
// the company's identity provider (Keycloak or Cognito); Enter the forge goes there and comes back to the Hearth.
export function ForgeGate({ calm }: { calm: boolean }) {
  const [going, setGoing] = useState(false)
  return (
    <main className="gate">
      {!calm && <Embers count={24} />}
      <div className="gate-inner">
        <p className="gate-brand"><span className="brand-mark" aria-hidden="true">⚒</span> Crucible</p>
        <p className="gate-motto">The crucible doesn't break the metal. It reveals it.</p>
        <Forging calm={calm} />
        <button type="button" className="primary big" disabled={going} onClick={() => { setGoing(true); window.location.assign('/auth/login') }}>
          {going ? 'Opening the gate…' : 'Enter the forge'}
        </button>
        <p className="gate-note">You sign in with your company account.</p>
      </div>
    </main>
  )
}
