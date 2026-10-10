import { useEffect, useState, type CSSProperties } from 'react'
import { CYCLE, HOLD, IMPACT, LAST, RESET, STAGES, next, sparks } from '../lib/forging'

const SPARKS = sparks()
const BLADE = 'M176 210 L180 202 L330 201 L354 206 L330 210 Z' // the sword's blade, also the masterwork sheen's clip
const HEAD = 'M231 152 Q231 146 234 141 L242 127 Q248 120 254 127 L262 141 Q265 146 265 152 L267 182 Q267 189 260 189 H236 Q229 189 229 182 Z'
const ECHOES = [1, 2, 3, 4] // the motion smear: copies of the head that follow the swing a little later each

// Gradients for the metal, all from theme tokens, so every theme forges in its own colours. Shared by the gate's animations.
export function Defs() {
  const stop = (offset: string, color: string) => <stop offset={offset} style={{ stopColor: color }} />
  return (
    <defs>
      <linearGradient id="forge-hot" x1="0" y1="0" x2="0" y2="1">{stop('0', 'var(--spark)')}{stop('0.35', 'var(--accent-2)')}{stop('1', 'var(--accent)')}</linearGradient>
      <linearGradient id="forge-temper" x1="0" y1="0" x2="1" y2="0">{stop('0', 'var(--accent)')}{stop('0.5', 'var(--accent-2)')}{stop('1', 'var(--spark)')}</linearGradient>
      <linearGradient id="forge-steel" x1="0" y1="0" x2="0" y2="1">{stop('0', 'var(--text)')}{stop('1', 'var(--muted)')}</linearGradient>
      <linearGradient id="forge-iron" x1="0" y1="0" x2="0" y2="1">{stop('0', 'var(--anvil)')}{stop('1', 'color-mix(in srgb, var(--anvil) 60%, var(--bg))')}</linearGradient>
      <linearGradient id="forge-hammer-steel" x1="0" y1="0" x2="1" y2="0">{stop('0', 'var(--text)')}{stop('0.45', 'color-mix(in srgb, var(--text) 55%, var(--muted))')}{stop('1', 'color-mix(in srgb, var(--muted) 70%, var(--anvil))')}</linearGradient>
      <linearGradient id="forge-wood" x1="0" y1="0" x2="0" y2="1">{stop('0', 'color-mix(in srgb, var(--accent) 55%, var(--anvil))')}{stop('1', 'color-mix(in srgb, var(--accent) 30%, var(--anvil))')}</linearGradient>
      <linearGradient id="forge-sheen" x1="0" y1="0" x2="1" y2="0">
        <stop offset="0" style={{ stopColor: 'var(--spark)', stopOpacity: 0 }} /><stop offset="0.5" style={{ stopColor: 'var(--spark)', stopOpacity: 0.95 }} /><stop offset="1" style={{ stopColor: 'var(--spark)', stopOpacity: 0 }} />
      </linearGradient>
      <clipPath id="forge-blade-clip"><path d={BLADE} /></clipPath>
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
          <path fill="url(#forge-steel)" d={BLADE} />
          <path className="forge-fuller" d="M186 206.3 L322 205.7" />
          <path className="forge-edge" d="M180 202 L330 201 L354 206" />
          <rect className="forge-guard" x="168" y="193" width="9" height="22" rx="2" />
          <rect className="forge-grip" x="141" y="202" width="28" height="7" rx="3" />
          <circle className="forge-guard" cx="136" cy="205.5" r="6" />
          {stage === LAST && (
            <>
              {/* a band of light glides along the blade, clipped to its shape, and the tip twinkles as it arrives */}
              <g clipPath="url(#forge-blade-clip)"><path className="forge-sheen" d="M150 188 L176 188 L162 224 L136 224 Z" fill="url(#forge-sheen)" /></g>
              <path className="forge-twinkle" d="M354 196 L356 204 L364 206 L356 208 L354 216 L352 208 L344 206 L352 204 Z" />
            </>
          )}
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
        {/* the motion smear: echoes of the head swing the same arc a moment behind it and show only on the way down, so
            the fall leaves a fading trail that closes up as the hammer lands */}
        {!calm && swing > 0 && ECHOES.map((n) => (
          <g key={`e${swing}-${n}`} className={`forge-echo e${n}`}><path d={HEAD} /></g>
        ))}
        <g className={!calm && swing > 0 ? 'forge-hammer swinging' : 'forge-hammer'} key={`w${swing}`}>
          <HammerShape />
        </g>
      </svg>
      <RankTrack stage={stage} said="An animation of a hammer forging ore into an ingot, a tempered bar, a blade, a sword and a masterwork: the forge's six ranks." />
    </div>
  )
}

// HammerShape is the hammer, drawn like the hammer among the user icons: rounded, poured metal with a white-hot
// highlight. Head face at (229–267, 189), handle pivot at (424, 173.5). Shared by the gate's animations.
export function HammerShape() {
  return (
    <>
      <path className="forge-handle" d="M266 167.5 H423 A6 6 0 0 1 423 179.5 H266 Z" />
      <path className="forge-grip-bands" d="M386 168 V179 M394 168 V179 M402 168 V179 M410 168 V179" />
      <circle className="forge-cap" cx="424" cy="173.5" r="6.5" />
      <path className="forge-head" d={HEAD} />
      <rect className="forge-head-core" x="235.5" y="143" width="4" height="40" rx="2" />
      <path className="forge-head-face" d="M229.5 184 H266.5 Q267 189 260 189 H236 Q229 189 229.5 184 Z" />
      <rect className="forge-collar" x="263" y="165.5" width="9" height="16" rx="2.5" />
    </>
  )
}

// RankTrack is the line of the six ranks under a gate animation, the current one lit, with its line below. Sighted
// people follow the animation; `said` tells a screen reader what it shows, once.
export function RankTrack({ stage, said }: { stage: number; said: string }) {
  return (
    <>
      <ol className="forge-ranks" aria-hidden="true">
        {STAGES.map((s, i) => (
          <li key={s.rank} className={i === stage ? 'now' : i < stage ? 'done' : undefined}>{s.rank}</li>
        ))}
      </ol>
      <p className="forge-line" aria-hidden="true" key={`l${stage}`}>{STAGES[stage].line}</p>
      <p className="sr-only">{said}</p>
    </>
  )
}
