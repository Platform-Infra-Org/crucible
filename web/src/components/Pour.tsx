import { useEffect, useState } from 'react'
import { LAST, POUR } from '../lib/forging'
import { Defs, RankTrack } from './Forging'

// The sword as one silhouette: pommel, grip, guard and blade, lying flat in its mould (pommel left, point right).
// It is the mould's engraved outline, the shape the metal fills, and the masterwork's sheen clip.
const SWORD = [
  'M149 222 a7 7 0 1 0 14 0 a7 7 0 1 0 -14 0', // pommel
  'M162 218.5 H197 V225.5 H162 Z', // grip
  'M196 207 Q196 205 198 205 H204 Q206 205 206 207 V237 Q206 239 204 239 H198 Q196 239 196 237 Z', // guard
  'M206 216 L398 215 L431 222 L398 229 L206 228 Z', // blade
].join(' ')
const CRUCIBLE = 'M64 64 H130 L124 124 Q122 138 108 138 H88 Q74 138 72 124 Z' // tilts about its spout's tip (140, 58)

// Pour is the gate's second animation, drawn in a few quiet lines: a crucible pours a sword mould. Ore drops in and
// the crucible warms (Ore); it tips, and molten metal runs through the mould from pommel to point (Ingot); the sword
// cools through the temper colours to steel (Tempered); the mould's outline fades and an edge is drawn (Blade); the
// sword rises free (Sword); it gleams (Masterwork). Each stage is a class on the drawing (s0…s5): plain rules hold
// where everything rests, entrance animations carry it there. Under calm motion it shows the masterwork, still. A new
// cycle remounts the drawing, so every animation starts clean.
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
          <clipPath id="pour-fill"><rect className="pour-fill" x="146" y="200" width="290" height="44" /></clipPath>
          <clipPath id="pour-sword"><path d={SWORD} /></clipPath>
        </defs>

        {/* the heat under the crucible: one ember line */}
        <path className="pour-ember" d="M72 154 H124" />

        {/* one piece of ore drops in, behind the crucible's wall once it is inside */}
        <path className="pour-ore" d="M88 96 L96 89 L106 92 L108 102 L98 108 L90 103 Z" />

        <g className="pour-crucible">
          <path className="pour-body" d={CRUCIBLE} />
          <path className="pour-heat" d={CRUCIBLE} />
          <path className="pour-lip" d="M62 64 H130 L140 58" />
          <path className="pour-surface" d="M68 66 H126" />
        </g>

        {/* the stream, falling into the mould at the pommel */}
        <path className="pour-stream" d="M141 60 C 151 98, 156 166, 156 214" />

        {/* the mould: the sword engraved as one thin line, on a faint bed */}
        <path className="pour-bed" d="M120 256 H460" />
        <path className="pour-mould" d={SWORD} />

        {/* the sword: the metal that fills the mould, then cools, comes free and gleams */}
        <g className="pour-sword">
          <g clipPath="url(#pour-fill)">
            <path fill="url(#forge-steel)" d={SWORD} />
            <path className="pour-temper" fill="url(#forge-temper)" d={SWORD} />
            <path className="pour-hot" fill="url(#forge-hot)" d={SWORD} />
          </g>
          <ellipse className="pour-front" cx="146" cy="222" rx="3" ry="16" />
          <path className="pour-edge" d="M206 216 L398 215 L431 222" />
          <g clipPath="url(#pour-sword)"><path className="pour-sheen" d="M150 196 L176 196 L160 248 L134 248 Z" fill="url(#forge-sheen)" /></g>
          <path className="pour-twinkle" d="M431 212 L433 220 L441 222 L433 224 L431 232 L429 224 L421 222 L429 220 Z" />
        </g>
      </svg>
      <RankTrack stage={s} said="An animation of a crucible pouring molten metal into a sword mould: it cools, comes free and gleams as a masterwork, through the forge's six ranks." />
    </div>
  )
}
