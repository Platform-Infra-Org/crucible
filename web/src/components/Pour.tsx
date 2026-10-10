import { useEffect, useState, type CSSProperties } from 'react'
import { LAST, POUR, sparks } from '../lib/forging'
import { Defs, HammerShape, RankTrack } from './Forging'

const SPARKS = sparks()
const BOWL = 'M44 40 H126 Q129 40 128 46 L121 106 Q117 128 85 128 Q53 128 49 106 L42 46 Q41 40 44 40 Z' // a clay crucible, tilting about its spout (137, 40)
const BLADE = 'M206 237 L210 230 L404 229 L431 234 L404 239 Z' // the cast in the mould; it lifts 30 px when it comes free
const WISPS = [240, 292, 344, 396] // steam rising from the quench

// Pour is the gate's second animation, the forge ranks as one story: ore falls into a crucible over the fire (Ore),
// the metal is poured into a blade mould and fills it end to end (Ingot), the cast is quenched in a cloud of steam
// (Tempered), the mould drops away and the blade comes free with an edge (Blade), the hilt goes on (Sword), and one
// hammer strike makes the masterwork, which gleams (Masterwork). Each stage is a class on the drawing (s0…s5): static
// rules hold where everything rests in that stage, entrance animations carry it there. Under calm motion it shows the
// masterwork, still. A new cycle remounts the drawing, so every animation starts clean.
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
      <svg key={cycle} className={`pour s${s}`} viewBox="0 -30 480 290" aria-hidden="true" focusable="false">
        <Defs />
        <defs>
          <clipPath id="pour-fill"><rect className="pour-fill" x="200" y="220" width="240" height="26" /></clipPath>
          <clipPath id="pour-blade"><path d={BLADE} /></clipPath>
        </defs>

        {/* the fire and the crucible's stand */}
        <ellipse className="pour-coals" cx="85" cy="160" rx="36" ry="4.5" />
        <g className="pour-fire">
          <path className="pour-flame f1" d="M66 158 Q60 146 68 136 Q70 146 76 150 Q76 140 82 130 Q90 144 86 158 Z" />
          <path className="pour-flame f2" d="M80 158 Q76 144 86 132 Q88 144 94 148 Q96 138 100 134 Q108 148 100 158 Z" />
          <path className="pour-flame f3" d="M94 158 Q92 148 98 140 Q102 150 106 150 Q108 144 110 140 Q116 150 110 158 Z" />
        </g>
        <path className="pour-stand" d="M55 128 L46 160 M115 128 L124 160 M36 160 H134" />

        {/* ore falling in, behind the crucible's front wall once it is inside */}
        <path className="pour-ore o1" d="M62 66 L66 58 L74 55 L82 59 L84 68 L70 71 Z" />
        <path className="pour-ore o2" d="M90 70 L94 61 L103 59 L110 64 L108 72 L96 74 Z" />

        {/* the crucible: dark iron that glows as the metal melts, a molten surface at the rim */}
        <g className="pour-crucible">
          <path className="pour-bowl" d={BOWL} />
          <path className="pour-heat" d={BOWL} />
          <path className="pour-spout" d="M130 40 L140 35 L137 45 Z" />
          <ellipse className="pour-surface" cx="85" cy="42" rx="43" ry="5" />
          <path className="pour-band" d="M45.5 70 Q85 76 124.5 70" />
          <path className="pour-rim" d="M42 40 H130" />
        </g>

        {/* the stream from the spout into the mould */}
        <path className="pour-stream" d="M138 42 C 154 66, 176 150, 214 232" />

        {/* the mould: a stone slab with a blade-shaped hollow */}
        <ellipse className="pour-glow" cx="318" cy="236" rx="150" ry="28" />
        <g className="pour-mould">
          <path className="pour-slab" d="M182 222 H446 L440 254 H188 Z" />
          <path className="pour-hollow" d="M203 238 L208 228.5 L405 227.5 L435 234 L405 240.5 Z" />
        </g>

        {/* the cast, which becomes the sword */}
        <g className="pour-cast">
          <g clipPath="url(#pour-fill)">
            <path fill="url(#forge-steel)" d={BLADE} />
            <path className="pour-temper" fill="url(#forge-temper)" d={BLADE} />
            <path className="pour-hot" fill="url(#forge-hot)" d={BLADE} />
          </g>
          <path className="pour-fuller" d="M216 234.2 L398 233.8" />
          <path className="pour-edge" d="M210 230 L404 229 L431 234" />
          <g className="pour-hilt">
            <rect className="forge-guard" x="197" y="223" width="9" height="22" rx="2" />
            <rect className="forge-grip" x="170" y="230" width="28" height="8" rx="3" />
            <circle className="forge-guard" cx="165" cy="234" r="6" />
          </g>
          <g clipPath="url(#pour-blade)"><path className="pour-sheen" d="M180 214 L206 214 L192 254 L166 254 Z" fill="url(#forge-sheen)" /></g>
          <path className="pour-twinkle" d="M431 224 L433 232 L441 234 L433 236 L431 244 L429 236 L421 234 L429 232 Z" />
        </g>

        {/* steam off the quench */}
        {WISPS.map((x, i) => (
          <path key={x} className={`pour-steam w${i + 1}`} d={`M${x} 226 q -6 -9 0 -18 q 6 -9 0 -18`} />
        ))}

        {/* the one strike that makes the masterwork: the hammer's face lands on the blade at (320, 199) */}
        <circle className="pour-flash" cx="320" cy="197" r="30" />
        {SPARKS.map((p, i) => (
          <circle key={i} className="pour-spark" cx="320" cy="197" r={p.r} style={{ '--dx': `${p.dx}px`, '--dy': `${p.dy}px` } as CSSProperties} />
        ))}
        <g transform="translate(72 10)"><g className="pour-hammer"><HammerShape /></g></g>
      </svg>
      <RankTrack stage={s} said="An animation of a crucible: ore is melted and poured into a blade mould, quenched, freed, given a hilt, and struck once into a masterwork, through the forge's six ranks." />
    </div>
  )
}
