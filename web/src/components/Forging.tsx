import { useEffect, useState, type CSSProperties } from 'react'
import { CYCLE, HOLD, IMPACT, LAST, RESET, STAGES, next, sparks } from '../lib/forging'

const SPARKS = sparks()

// Gradients for the metal, all from theme tokens, so every theme forges in its own colours.
function Defs() {
  const stop = (offset: string, color: string) => <stop offset={offset} style={{ stopColor: color }} />
  return (
    <defs>
      <linearGradient id="forge-hot" x1="0" y1="0" x2="0" y2="1">{stop('0', 'var(--spark)')}{stop('0.35', 'var(--accent-2)')}{stop('1', 'var(--accent)')}</linearGradient>
      <linearGradient id="forge-temper" x1="0" y1="0" x2="1" y2="0">{stop('0', 'var(--accent)')}{stop('0.5', 'var(--accent-2)')}{stop('1', 'var(--spark)')}</linearGradient>
      <linearGradient id="forge-steel" x1="0" y1="0" x2="0" y2="1">{stop('0', 'var(--text)')}{stop('1', 'var(--muted)')}</linearGradient>
      <linearGradient id="forge-iron" x1="0" y1="0" x2="0" y2="1">{stop('0', 'var(--anvil)')}{stop('1', 'color-mix(in srgb, var(--anvil) 60%, var(--bg))')}</linearGradient>
      <radialGradient id="forge-glow">{stop('0', 'var(--accent-2)')}<stop offset="1" style={{ stopColor: 'var(--accent)', stopOpacity: 0 }} /></radialGradient>
      <radialGradient id="forge-flash">{stop('0', 'var(--spark)')}<stop offset="1" style={{ stopColor: 'var(--accent-2)', stopOpacity: 0 }} /></radialGradient>
    </defs>
  )
}

// One piece per rank, resting on the anvil's face (y = 210), centred near x = 250.
function Piece({ stage }: { stage: number }) {
  switch (stage) {
    case 0: // ore: a rough lump with embers in its cracks
      return (
        <g>
          <path className="forge-ore" d="M205 210 L211 193 L226 183 L245 178 L262 183 L280 180 L294 192 L299 210 Z" />
          <path className="forge-crack" d="M222 200 L236 190 L250 196 M258 190 L270 198 L284 192" />
          <circle className="forge-ember" cx="240" cy="200" r="2" /><circle className="forge-ember" cx="272" cy="188" r="1.6" /><circle className="forge-ember" cx="228" cy="192" r="1.4" />
        </g>
      )
    case 1: // ingot: a cast bar, its top face catching the heat
      return (
        <g>
          <path fill="url(#forge-hot)" d="M208 210 L220 191 L280 191 L292 210 Z" />
          <path className="forge-face" d="M220 191 L226 184 L274 184 L280 191 Z" />
        </g>
      )
    case 2: // tempered: drawn longer, the temper colours running along it
      return <path fill="url(#forge-temper)" d="M186 210 L193 197 L307 197 L314 210 Z" />
    case 3: // blade: hammered thin and tapered to a point
      return <path fill="url(#forge-hot)" d="M160 210 L168 202 L318 200 L348 206 L320 210 Z" />
    default: // sword (4) and masterwork (5): cooled steel, a guard, a grip and a pommel
      return (
        <g className={stage === LAST ? 'forge-master' : undefined}>
          <path fill="url(#forge-steel)" d="M176 210 L180 202 L330 201 L354 206 L330 210 Z" />
          <rect className="forge-guard" x="168" y="193" width="9" height="22" rx="2" />
          <rect className="forge-grip" x="141" y="202" width="28" height="7" rx="3" />
          <circle className="forge-guard" cx="136" cy="205.5" r="6" />
          {stage === LAST && <rect className="forge-gleam" x="180" y="199" width="16" height="12" rx="3" />}
        </g>
      )
  }
}

// Forging is the gate's animation: a hammer strikes ore into an ingot, a tempered bar, a blade, a sword and a
// masterwork, the forge's six ranks, then rests and starts again. Under calm motion it shows the masterwork, still.
export function Forging({ calm }: { calm: boolean }) {
  const [shown, setShown] = useState(calm ? LAST : 0)
  const [swing, setSwing] = useState(0) // a new value restarts the hammer's swing

  useEffect(() => {
    if (calm) return
    let timer: ReturnType<typeof setTimeout>
    const strike = (stage: number) => {
      if (stage >= LAST) { // rest on the masterwork, let it cool back to ore, begin again
        timer = setTimeout(() => { setShown(0); timer = setTimeout(() => strike(0), RESET) }, HOLD)
        return
      }
      setSwing((s) => s + 1)
      timer = setTimeout(() => { setShown(next(stage)); timer = setTimeout(() => strike(next(stage)), CYCLE - IMPACT) }, IMPACT)
    }
    timer = setTimeout(() => strike(0), 700)
    return () => clearTimeout(timer)
  }, [calm])

  const stage = calm ? LAST : shown
  return (
    <div className="forging">
      <svg viewBox="0 0 480 290" aria-hidden="true" focusable="false">
        <Defs />
        <ellipse className="forge-heat" cx="250" cy="208" rx="120" ry="34" key={`h${stage}`} />
        {/* the anvil */}
        <path className="forge-anvil" d="M150 210 L370 210 L370 226 L150 226 Z M150 210 L104 214 Q 90 218 104 222 L150 226 Z" />
        <path className="forge-anvil" d="M190 226 L330 226 L312 252 L208 252 Z" />
        <path className="forge-anvil" d="M176 252 L344 252 L352 270 L168 270 Z" />
        <path className="forge-anvil-edge" d="M104 214 L150 210 L370 210" />
        {/* the piece, squashed into shape by each strike */}
        <g className="forge-piece" key={`p${stage}`}><Piece stage={stage} /></g>
        {/* the strike: a flash and sparks at the point of impact */}
        {!calm && shown > 0 && (
          <g key={`s${shown}`}>
            <circle className="forge-flash" cx="250" cy="190" r="34" />
            {SPARKS.map((s, i) => (
              <circle key={i} className="forge-spark" cx="250" cy="188" r={s.r} style={{ '--dx': `${s.dx}px`, '--dy': `${s.dy}px` } as CSSProperties} />
            ))}
          </g>
        )}
        {/* the hammer: its handle pivots at the right; at rest it is raised */}
        <g className={!calm && swing > 0 ? 'forge-hammer swinging' : 'forge-hammer'} key={`w${swing}`}>
          <rect className="forge-handle" x="262" y="169" width="160" height="9" rx="4.5" />
          <path className="forge-head" d="M232 138 L264 138 L267 145 L267 181 L271 188 L225 188 L229 181 L229 145 Z" />
          <rect className="forge-head-face" x="225" y="183" width="46" height="5" rx="1.5" />
        </g>
      </svg>
      <ol className="forge-ranks" aria-hidden="true">
        {STAGES.map((s, i) => (
          <li key={s.rank} className={i === stage ? 'now' : i < stage ? 'done' : undefined}>{s.rank}</li>
        ))}
      </ol>
      <p className="forge-line" aria-hidden="true" key={`l${stage}`}>{STAGES[stage].line}</p>
      <p className="sr-only">An animation of a hammer forging ore into an ingot, a tempered bar, a blade, a sword and a masterwork: the forge's six ranks.</p>
    </div>
  )
}
