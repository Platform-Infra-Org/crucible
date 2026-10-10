import { useEffect, useState } from 'react'
import { LAST, POUR } from '../lib/forging'
import { Defs, RankTrack } from './Forging'

// Everything is symmetric about x = 240. The sword stands upright in its mould, hilt up, point down, so the metal is
// poured in at the pommel and runs down to the point. It is the mould's engraved outline, the shape the metal fills
// and the masterwork's sheen clip.
const SWORD = [
  'M232 96 a8 8 0 1 0 16 0 a8 8 0 1 0 -16 0', // pommel
  'M235.5 103 H244.5 V134 H235.5 Z', // grip
  'M204 139 Q204 132 213 134 H267 Q276 132 276 139 Q276 146 268 144 H212 Q204 146 204 139 Z', // guard, its ends swept
  'M229 144 H251 L249 244 L240 272 L231 244 Z', // blade
].join(' ')
const BOWL = 'M198 14 Q240 8 282 14 L274 38 Q269 54 240 54 Q211 54 206 38 Z' // the crucible, seen from the front

// One rune per rank around the arcane circle (centre 240,190; radius 87), lit in turn: left, upper left, upper right,
// right, lower right, lower left. Left and right mirror each other. Glyphs are drawn around their own centre.
const RUNES = [
  { x: 153, y: 190, d: 'M0 -7 V7 M0 -2 L5 -7' },
  { x: 196.5, y: 114.7, d: 'M-4 7 L0 -7 L4 7' },
  { x: 283.5, y: 114.7, d: 'M-4 -7 L0 7 L4 -7' },
  { x: 327, y: 190, d: 'M0 -7 V7 M0 -2 L-5 -7' },
  { x: 283.5, y: 265.3, d: 'M-4 -7 L4 7 M4 -7 L-4 7' },
  { x: 196.5, y: 265.3, d: 'M0 -7 V7 M-5 -1 H5' },
]

// Pour is the gate's second animation, a rune forge: a crucible floating over an arcane circle pours an upright sword
// mould. Ore drops in and the crucible's runes wake (Ore); a stream falls into the pommel and the metal fills the sword
// to its point (Ingot); it cools through the temper colours to steel (Tempered); the mould fades and both edges are
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

        {/* the arcane circle: an aura, an outer ring, a slowly turning inner ring, and a rune for each rank */}
        <circle className="pour-aura" cx="240" cy="190" r="78" />
        <circle className="pour-ring" cx="240" cy="190" r="96" />
        <circle className="pour-ring-inner" cx="240" cy="190" r="78" />
        {RUNES.map((r, i) => (
          <g key={i} transform={`translate(${r.x} ${r.y})`}>
            <path className={i < s ? 'pour-rune lit' : i === s ? 'pour-rune lit now' : 'pour-rune'} d={r.d} />
          </g>
        ))}

        {/* the crucible floats, bobbing; ore drops in through the molten surface */}
        <path className="pour-ore" d="M234 6 L240 0 L247 3 L248 11 L241 15 L235 12 Z" />
        <g className="pour-crucible">
          <path className="pour-body" d={BOWL} />
          <path className="pour-heat" d={BOWL} />
          <ellipse className="pour-surface" cx="240" cy="14" rx="40" ry="4.5" />
          <ellipse className="pour-rim" cx="240" cy="14" rx="42" ry="5.5" />
          <path className="pour-glyphs" d="M220 26 V38 M216 30 L224 34 M240 25 V39 M236 29 H244 M260 26 V38 M256 34 L264 30" />
          <path className="pour-spout" d="M233 52 L240 62 L247 52 Z" />
        </g>

        {/* the stream falls straight into the pommel */}
        <path className="pour-stream" d="M240 62 V90" />

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
      <RankTrack stage={s} said="An animation of a rune forge: a floating crucible pours molten metal into an upright sword mould inside an arcane circle; the sword cools, rises and gleams as a masterwork, one rune lighting for each of the forge's six ranks." />
    </div>
  )
}
