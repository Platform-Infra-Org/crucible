import { useEffect, useState, type CSSProperties } from 'react'
import { LAST, RUNE_STEP, bolt } from '../lib/forging'
import { Defs, RankTrack } from './Forging'

// The forge's centre is (240,142): a ring of stone blocks between iron rims (92–118), six runestones set in it, a
// stepped plinth under it. Everything forged sits upright inside the ring, centred on x = 240.
const C = { x: 240, y: 142 }
const at = (deg: number, r: number) => {
  const a = (deg * Math.PI) / 180
  return { x: +(C.x + r * Math.cos(a)).toFixed(1), y: +(C.y + r * Math.sin(a)).toFixed(1) }
}

// One runestone per rank, lit in turn round the circle: lower left, up and over the top, down to lower right. The
// glyphs are Elder Futhark (fehu, uruz, thurisaz, algiz, kenaz, tiwaz), drawn around their own centre.
const RUNES = [
  { deg: 120, d: 'M-3 -9 V9 M-3 -3 L5 -9 M-3 3 L5 -3' },
  { deg: 180, d: 'M-5 9 V-9 L5 -2 V9' },
  { deg: 240, d: 'M-3 -9 V9 M-3 -5 L4 0 L-3 5' },
  { deg: 300, d: 'M0 9 V-9 M-6 -8 L0 -2 L6 -8' },
  { deg: 0, d: 'M4 -8 L-4 0 L4 8' },
  { deg: 60, d: 'M0 -9 V9 M-6 -3 L0 -9 L6 -3' },
].map((r) => ({ ...r, ...at(r.deg, 105) }))
const KEYSTONE = 'M326 130 L368 127 Q372 142 368 157 L326 154 Q323 142 326 130 Z' // pointing right, turned into place

// The ring's other blocks: a joint every 20° between the runestones, an iron rivet at each on the outer rim.
const JOINT_DEGS = Array.from({ length: 18 }, (_, i) => i * 20).filter((d) => d % 60 !== 0)
const JOINTS = JOINT_DEGS.map((d) => { const a = at(d, 92), b = at(d, 118); return `M${a.x} ${a.y} L${b.x} ${b.y}` }).join(' ')
const RIVETS = JOINT_DEGS.map((d) => at(d, 118))
const STAR = 'M326 142 L197 216.5 L197 67.5 Z M154 142 L283 67.5 L283 216.5 Z' // a hexagram on the runes' angles

// The sword, upright, point down. Its parts are also the shapes the earlier ranks are made from.
const BLADE = 'M230 108 H250 L248 192 L240 216 L232 192 Z'
const TANG = 'M236 80 H244 V108 H236 Z'
const SWORD = [
  'M233.5 72 a6.5 6.5 0 1 0 13 0 a6.5 6.5 0 1 0 -13 0', // pommel
  'M236 78 H244 V100 H236 Z', // grip
  'M206 104 Q206 99 213 100 H267 Q274 99 274 104 Q274 109 267 108 H213 Q206 109 206 104 Z', // guard
  BLADE,
].join(' ')

// Motes that orbit the enchanted blade at different heights, each on its own ellipse and clock, their starting points
// spread round the orbit (d is a share of the 2t lap) so they never bunch on one side.
const MOTES = [
  { y: 82, rx: 22, ry: 5, t: 1.2, d: 0, r: 1.8 },
  { y: 98, rx: 40, ry: 7, t: 1.6, d: -1.76, r: 2.4 },
  { y: 116, rx: 30, ry: 6, t: 1.3, d: -0.78, r: 2 },
  { y: 134, rx: 36, ry: 7, t: 1.7, d: -2.72, r: 2.6 },
  { y: 152, rx: 28, ry: 6, t: 1.25, d: -0.38, r: 1.9 },
  { y: 170, rx: 34, ry: 7, t: 1.5, d: -1.95, r: 2.3 },
  { y: 188, rx: 24, ry: 5, t: 1.15, d: -0.92, r: 1.8 },
  { y: 204, rx: 18, ry: 4, t: 1.35, d: -2.43, r: 1.6 },
]

// The piece in the circle at each rank: ore, an ingot, a tempered bar, a blade with its tang, the sword.
function Piece({ n }: { n: number }) {
  switch (n) {
    case 0:
      return (
        <>
          <path className="forge-ore" d="M216 160 L220 140 L232 128 L246 126 L258 134 L264 148 L260 164 L246 170 L228 168 Z" />
          <path className="forge-crack" d="M226 152 L236 143 L246 149 M249 138 L255 153" />
          <circle className="forge-ember" cx="238" cy="157" r="2" /><circle className="forge-ember" cx="252" cy="145" r="1.6" /><circle className="forge-ember" cx="230" cy="142" r="1.4" />
        </>
      )
    case 1:
      return (
        <>
          <path fill="url(#forge-hot)" d="M222 172 L229 120 H251 L258 172 Z" />
          <path className="forge-face" d="M229 120 L233 113 H247 L251 120 Z" />
        </>
      )
    case 2:
      return <path fill="url(#rf-temper)" d="M233 92 H247 Q251 92 251 96 V200 Q251 204 247 204 H233 Q229 204 229 200 V96 Q229 92 233 92 Z" />
    case 3:
      return <path fill="url(#forge-hot)" d={`${TANG} ${BLADE}`} />
    default:
      return (
        <>
          <path fill="url(#forge-steel)" d={SWORD} />
          <path className="rf-fuller" d="M240 116 V186" />
          <path className="rf-edge" d="M230 108 L232 192 L240 216 L248 192 L250 108" />
          <circle className="rf-gem" cx="240" cy="72" r="2.8" />
        </>
      )
  }
}

// RuneForge is the gate's second animation: a rune forge, a stone ring of runestones on a plinth. Each rank lights
// the next rune round the circle; its beam strikes the centre, where the piece changes: ore, ingot, tempered bar,
// blade, sword. With the sixth rune the circle is complete: its rims ignite all the way round, every rune strikes the
// sword at once, a shockwave rolls out, and the masterwork stays enchanted, motes and lightning circling the blade.
// Each stage is a class on the drawing (s0…s5): plain rules hold what a stage shows, entrance animations carry it
// there, so calm motion shows the enchanted masterwork, still (lightning frozen, no motes).
export function RuneForge({ calm }: { calm: boolean }) {
  const [stage, setStage] = useState(calm ? LAST : 0)

  useEffect(() => {
    if (calm) return
    let timer: ReturnType<typeof setTimeout>
    const go = (s: number) => {
      if (s < LAST) timer = setTimeout(() => { setStage(s + 1); go(s + 1) }, RUNE_STEP[s])
    }
    go(0)
    return () => clearTimeout(timer)
  }, [calm])

  const s = calm ? LAST : stage
  const done = s === LAST
  const stone = (offset: string, color: string) => <stop offset={offset} style={{ stopColor: color }} />
  return (
    <div className="forging">
      <svg className={`rune-forge s${s}`} viewBox="0 0 480 290" aria-hidden="true" focusable="false">
        <Defs />
        <defs>
          <radialGradient id="rf-stone" gradientUnits="userSpaceOnUse" cx={C.x} cy={C.y} r="128">
            {stone('0.72', 'color-mix(in srgb, var(--anvil) 60%, var(--muted))')}{stone('0.8', 'var(--anvil)')}
            {stone('0.87', 'color-mix(in srgb, var(--anvil) 78%, var(--muted))')}{stone('0.92', 'var(--anvil)')}
            {stone('1', 'color-mix(in srgb, var(--anvil) 70%, var(--bg))')}
          </radialGradient>
          <linearGradient id="rf-plinth" x1="0" y1="0" x2="0" y2="1">{stone('0', 'color-mix(in srgb, var(--anvil) 70%, var(--muted))')}{stone('1', 'var(--anvil)')}</linearGradient>
          <linearGradient id="rf-temper" x1="0" y1="0" x2="0" y2="1">
            {stone('0', 'color-mix(in srgb, var(--accent) 60%, var(--muted))')}{stone('0.5', 'var(--accent-2)')}{stone('1', 'color-mix(in srgb, var(--spark) 70%, var(--muted))')}
          </linearGradient>
          <clipPath id="rf-sword"><path d={SWORD} /></clipPath>
          <linearGradient id="rf-sheen" x1="0" y1="0" x2="0" y2="1">
            <stop offset="0" style={{ stopColor: 'var(--spark)', stopOpacity: 0 }} /><stop offset="0.5" style={{ stopColor: 'var(--spark)', stopOpacity: 0.95 }} /><stop offset="1" style={{ stopColor: 'var(--spark)', stopOpacity: 0 }} />
          </linearGradient>
        </defs>

        {/* the plinth the ring stands on, a rune seam carved along its step */}
        <path className="rf-plinth" d="M190 238 H290 L298 254 H182 Z" />
        <path className="rf-plinth" d="M158 254 H322 L332 284 H148 Z" />
        <path className="rf-seam" d="M176 269 H304" />

        {/* the ring: a sunken floor, its glow, a hexagram and a turning ring of glyph marks inside; stone blocks between
            iron rims; a runestone for each rank */}
        <circle className="rf-floor" cx={C.x} cy={C.y} r="92" />
        <circle className="rf-aura" cx={C.x} cy={C.y} r="90" />
        <path className="rf-star" d={STAR} />
        <circle className="rf-glyph-ring" cx={C.x} cy={C.y} r="76" />
        <circle className="rf-stone" cx={C.x} cy={C.y} r="105" />
        <path className="rf-joints" d={JOINTS} />
        <circle className="rf-iron" cx={C.x} cy={C.y} r="92" />
        <circle className="rf-iron outer" cx={C.x} cy={C.y} r="118" />
        {RIVETS.map((p, i) => <circle key={i} className="rf-rivets" cx={p.x} cy={p.y} r="1.7" />)}
        {RUNES.map((r, i) => (
          <g key={i}>
            <path className={i <= s ? 'rf-key lit' : 'rf-key'} d={KEYSTONE} transform={`rotate(${r.deg} ${C.x} ${C.y})`} />
            <g transform={`translate(${r.x} ${r.y})`}>
              <path className={i < s ? 'rf-rune lit' : i === s ? 'rf-rune lit now' : 'rf-rune'} d={r.d} />
            </g>
          </g>
        ))}

        {/* the circle complete: both rims ignite all the way round, from the first rune */}
        {done && (
          <>
            <circle className="rf-close" cx={C.x} cy={C.y} r="92" pathLength={1} transform={`rotate(120 ${C.x} ${C.y})`} />
            <circle className="rf-close outer" cx={C.x} cy={C.y} r="118" pathLength={1} transform={`rotate(120 ${C.x} ${C.y})`} />
            <g className="rf-enchant"><ellipse className="rf-sword-aura" cx="240" cy="144" rx="38" ry="92" /></g>
          </>
        )}

        {/* the piece, changed by each rune */}
        {[0, 1, 2, 3, 4].map((n) => <g key={n} className={`rf-piece rf-p${n}`}><Piece n={n} /></g>)}

        {/* the strike: the newest rune's beam, a flash at the centre; at the masterwork every rune strikes and a
            shockwave rolls out */}
        {!calm && (
          <g key={`strike${s}`}>
            <path className="rf-beam" d={`M${RUNES[s].x} ${RUNES[s].y} L${C.x} ${C.y}`} pathLength={1} />
            <circle className="rf-flash" cx={C.x} cy={C.y} r="40" />
            {done && RUNES.map((r, i) => <path key={i} className="rf-beam all" d={`M${r.x} ${r.y} L${C.x} ${C.y}`} pathLength={1} />)}
            {done && <><circle className="rf-flash big" cx={C.x} cy={C.y} r="70" /><circle className="rf-burst" cx={C.x} cy={C.y} r="26" /></>}
          </g>
        )}

        {/* the enchantment: lightning running round the blade, motes orbiting it, a sheen down the steel */}
        {done && (
          <g className="rf-enchant">
            <path className="rf-bolt" d={bolt(3)} pathLength={100} />
            <path className="rf-bolt b2" d={bolt(5)} pathLength={100} />
            {!calm && MOTES.map((m, i) => (
              <g key={i} className="rf-mote" style={{ '--rx': `${m.rx}px`, '--ry': `${m.ry}px`, '--t': `${m.t}s`, '--d': `${m.d}s` } as CSSProperties}>
                <circle cx="240" cy={m.y} r={m.r} />
              </g>
            ))}
            <g clipPath="url(#rf-sword)"><rect className="rf-sheen" x="200" y="46" width="80" height="22" fill="url(#rf-sheen)" /></g>
            <path className="rf-twinkle" d="M240 206 L242 214 L250 216 L242 218 L240 226 L238 218 L230 216 L238 214 Z" />
          </g>
        )}
      </svg>
      <RankTrack stage={s} said="An animation of a rune forge: a ring of runestones lights one rune for each of the forge's six ranks, each changing the metal in its centre from ore to ingot, tempered bar, blade and sword; when the circle is complete the sword becomes a masterwork, enchanted, with motes and lightning circling the blade." />
    </div>
  )
}
