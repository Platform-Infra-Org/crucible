import { useEffect, useState } from 'react'
import { LAST, POUR } from '../lib/forging'
import { Defs, RankTrack } from './Forging'

// The circle and the sword are symmetric about x = 240. The sword stands upright in its mould, hilt up, point down, so the metal is
// poured in at the pommel and runs down to the point. It is the mould's engraved outline, the shape the metal fills
// and the masterwork's sheen clip.
const SWORD = [
  'M232 96 a8 8 0 1 0 16 0 a8 8 0 1 0 -16 0', // pommel
  'M235.5 103 H244.5 V134 H235.5 Z', // grip
  'M204 139 Q204 132 213 134 H267 Q276 132 276 139 Q276 146 268 144 H212 Q204 146 204 139 Z', // guard, its ends swept
  'M229 144 H251 L249 244 L240 272 L231 244 Z', // blade
].join(' ')
// The crucible in its own coordinates, pivot at the origin; it hovers up and to the left of the circle with its lip
// toward it, and tilts 45° about the pivot to pour, which brings the lip's tip over the pommel (x = 240).
const BOWL = 'M-36 -20 Q0 -26 36 -20 L28 12 Q24 26 0 26 Q-24 26 -28 12 Z'

// The rune circle (centre 240,184): a carved band between two rings, the six runes set in it at radius 91, lit in turn
// for each rank: left, upper left, upper right, right, lower right, lower left. A hexagram joins their points inside.
// The glyphs are Elder Futhark (fehu, uruz, thurisaz, algiz, kenaz, tiwaz), drawn around their own centre.
const RUNES = [
  { x: 149, y: 184, d: 'M-3 -8 V8 M-3 -3 L4 -8 M-3 2 L4 -3' },
  { x: 194.5, y: 105.2, d: 'M-4 8 V-8 L4 -2 V8' },
  { x: 285.5, y: 105.2, d: 'M-3 -8 V8 M-3 -4 L3 0 L-3 4' },
  { x: 331, y: 184, d: 'M0 8 V-8 M-5 -7 L0 -1 L5 -7' },
  { x: 285.5, y: 262.8, d: 'M3 -7 L-3 0 L3 7' },
  { x: 194.5, y: 262.8, d: 'M0 -8 V8 M-5 -3 L0 -8 L5 -3' },
]
const STAR = 'M158 184 L281 113 L281 255 Z M322 184 L199 113 L199 255 Z'

// Pour is the gate's second animation, a rune forge: a crucible floating beside a rune circle tilts and pours an upright
// sword mould. Ore drops in and the crucible's runes wake (Ore); it tips, a stream falls from its lip into the pommel and
// the metal fills the sword to its point (Ingot); it cools through the temper colours to steel (Tempered); the mould fades and both edges are
// drawn (Blade); the sword rises in the circle (Sword); the circle blazes and the steel gleams (Masterwork). Each rank
// lights one rune. Each stage is a class on the drawing (s0…s5): plain rules hold where everything rests, entrance
// animations carry it there, so calm motion shows the masterwork, still. A new cycle remounts the drawing.
export function Pour({ calm }: { calm: boolean }) {
  const [stage, setStage] = useState(calm ? LAST : 0)
  const [cycle, setCycle] = useState(0)

  useEffect(() => {
    if (calm) return
    let timer: ReturnType<typeof setTimeout>
    const go = (s: number) => {
      timer = setTimeout(() => {
        if (s >= LAST) { setCycle((c) => c + 1); setStage(0); go(0) } else { setStage(s + 1); go(s + 1) }
      }, POUR[s])
    }
    go(0)
    return () => clearTimeout(timer)
  }, [calm])

  const s = calm ? LAST : stage
  return (
    <div className="forging">
      <svg key={cycle} className={`pour s${s}`} viewBox="0 0 480 290" aria-hidden="true" focusable="false">
        <Defs />
        <defs>
          <clipPath id="pour-fill"><rect className="pour-fill" x="200" y="86" width="80" height="190" /></clipPath>
          <clipPath id="pour-sword"><path d={SWORD} /></clipPath>
          <linearGradient id="pour-sheen-v" x1="0" y1="0" x2="0" y2="1">
            <stop offset="0" style={{ stopColor: 'var(--spark)', stopOpacity: 0 }} /><stop offset="0.5" style={{ stopColor: 'var(--spark)', stopOpacity: 0.95 }} /><stop offset="1" style={{ stopColor: 'var(--spark)', stopOpacity: 0 }} />
          </linearGradient>
        </defs>

        {/* the rune circle: an aura, a carved band between two rings with a bezel of ticks, a hexagram, a slowly turning
            inner ring, and a rune for each rank set in the band */}
        <circle className="pour-aura" cx="240" cy="184" r="82" />
        <circle className="pour-band" cx="240" cy="184" r="91" />
        <circle className="pour-ticks" cx="240" cy="184" r="104" />
        <circle className="pour-ring" cx="240" cy="184" r="100" />
        <circle className="pour-ring-in" cx="240" cy="184" r="82" />
        <path className="pour-star" d={STAR} />
        <circle className="pour-ring-inner" cx="240" cy="184" r="68" />
        {RUNES.map((r, i) => (
          <g key={i} transform={`translate(${r.x} ${r.y})`}>
            <path className={i < s ? 'pour-rune lit' : i === s ? 'pour-rune lit now' : 'pour-rune'} d={r.d} />
          </g>
        ))}

        {/* the stream falls from the tilted lip into the pommel, behind the crucible */}
        <path className="pour-stream" d="M240 58 V90" />

        {/* the crucible floats, bobbing; ore drops in through the molten surface; it tilts about its pivot to pour */}
        <path className="pour-ore" d="M181 6 L187 0 L194 3 L195 11 L188 15 L182 12 Z" />
        <g className="pour-crucible">
          <g transform="translate(187 48)">
            <g className="pour-tilt">
              <path className="pour-spout" d="M31 -24 L47 -28 L35 -15 Z" />
              <path className="pour-body" d={BOWL} />
              <path className="pour-heat" d={BOWL} />
              <ellipse className="pour-surface" cx="0" cy="-20" rx="33" ry="4" />
              <ellipse className="pour-rim" cx="0" cy="-20" rx="36" ry="5" />
              <path className="pour-glyphs" d="M-16 -10 V4 M-16 -6 L-11 -10 M-16 -2 L-11 -6 M0 -10 V4 M-5 -5 L0 -10 L5 -5 M14 -10 V4 M14 -6 L19 -3 L14 0" />
            </g>
          </g>
        </g>

        {/* the mould: the upright sword, engraved as one line */}
        <path className="pour-mould" d={SWORD} />

        {/* the sword: the metal that fills the mould, cools, rises and gleams */}
        <g className="pour-sword">
          <g clipPath="url(#pour-fill)">
            <path fill="url(#forge-steel)" d={SWORD} />
            <path className="pour-temper" fill="url(#forge-temper)" d={SWORD} />
            <path className="pour-hot" fill="url(#forge-hot)" d={SWORD} />
          </g>
          <ellipse className="pour-front" cx="240" cy="88" rx="18" ry="3.5" />
          <path className="pour-fuller" d="M240 152 V236" />
          <path className="pour-edge" d="M229 144 L231 244 L240 272 L249 244 L251 144" />
          <circle className="pour-gem" cx="240" cy="96" r="3.6" />
          <g clipPath="url(#pour-sword)"><rect className="pour-sheen" x="196" y="78" width="88" height="22" fill="url(#pour-sheen-v)" /></g>
          <path className="pour-twinkle" d="M240 262 L242 270 L250 272 L242 274 L240 282 L238 274 L230 272 L238 270 Z" />
        </g>
      </svg>
      <RankTrack stage={s} said="An animation of a rune forge: a floating crucible tilts and pours molten metal into an upright sword mould set in a rune circle; the sword cools, rises and gleams as a masterwork, one rune lighting for each of the forge's six ranks." />
    </div>
  )
}
