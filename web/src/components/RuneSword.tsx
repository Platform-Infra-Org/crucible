import { useEffect, useState, type CSSProperties } from 'react'
import { LAST, RUNE_STEP, bolt } from '../lib/forging'
import { Defs, RankTrack } from './Forging'

// A legendary sword lying horizontal, point to the right, centred on (245,150): a faceted pommel, a wrapped grip, a
// swept crossguard with a gem at its heart, and a broad bevelled blade with a fuller down its middle where the runes
// are engraved.
const BLADE = 'M136 129 H392 Q424 136 452 150 Q424 164 392 171 H136 Z' // also the sheen's clip
const FULLER = 'M152 141 H380 Q388 150 380 159 H152 Q148 150 152 141 Z'
const GUARD = 'M116 141 Q112 118 120 100 Q123 93 130 96 Q125 104 126 118 Q127 132 136 138 V162 Q127 168 126 182 Q125 196 130 204 Q123 207 120 200 Q112 182 116 159 Z'
const WRAPS = Array.from({ length: 7 }, (_, i) => `M${64 + i * 7} 144 L${70 + i * 7} 156`).join(' ')

// One rune per rank along the fuller, guard to tip, each lighting in its own colour (--rune-1 … --rune-6). The glyphs
// are Elder Futhark (fehu, uruz, thurisaz, algiz, kenaz, tiwaz), drawn around their own centre.
const RUNES = [
  'M-3 -7 V7 M-3 -2 L4 -7 M-3 3 L4 -2',
  'M-4 7 V-7 L4 -1 V7',
  'M-3 -7 V7 M-3 -4 L3 0 L-3 4',
  'M0 7 V-7 M-5 -6 L0 -1 L5 -6',
  'M3 -6 L-3 0 L3 6',
  'M0 -7 V7 M-5 -2 L0 -7 L5 -2',
].map((d, i) => ({ d, x: 170 + i * 38, c: `var(--rune-${i + 1})` }))

// Motes circling the enchanted sword, each on its own ellipse round it and its own clock, starting points spread
// round the lap (d is a share of the 2t lap) so they never bunch; tone picks spark, blue or violet.
const MOTES = [
  { rx: 222, ry: 60, dy: -4, t: 2.6, d: 0, r: 2.6, tone: 1 },
  { rx: 196, ry: 46, dy: 6, t: 2.2, d: -2.9, r: 2, tone: 2 },
  { rx: 170, ry: 66, dy: -8, t: 2.9, d: -1.6, r: 2.4, tone: 0 },
  { rx: 210, ry: 38, dy: 2, t: 2.0, d: -3.4, r: 1.8, tone: 1 },
  { rx: 150, ry: 54, dy: 8, t: 2.4, d: -0.7, r: 2.2, tone: 2 },
  { rx: 228, ry: 72, dy: 0, t: 3.2, d: -4.5, r: 2.8, tone: 0 },
  { rx: 184, ry: 30, dy: -2, t: 1.8, d: -2.2, r: 1.7, tone: 2 },
  { rx: 132, ry: 44, dy: 4, t: 2.1, d: -1.2, r: 2, tone: 1 },
  { rx: 204, ry: 58, dy: -6, t: 2.7, d: -4.1, r: 2.3, tone: 2 },
  { rx: 160, ry: 34, dy: 10, t: 1.9, d: -0.4, r: 1.8, tone: 0 },
]

// RuneSword is the gate's second animation: a legendary runic sword. Each rank lights the next of six runes engraved
// along its fuller, each in its own colour, ember to violet. With the sixth the sword awakens: light surges down the
// fuller, a shockwave rolls out, and it stays enchanted, a blue-violet aura round it, motes circling it and lightning
// running about it. Each stage is a class on the drawing (s0…s5): plain rules hold what a stage shows, entrance
// animations carry it there, so calm motion shows the enchanted masterwork, still (lightning frozen, no motes).
export function RuneSword({ calm }: { calm: boolean }) {
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
  const stop = (offset: string, color: string, opacity = 1) => <stop offset={offset} style={{ stopColor: color, stopOpacity: opacity }} />
  return (
    <div className="forging">
      <svg className={`rune-sword s${s}`} viewBox="0 0 480 290" aria-hidden="true" focusable="false">
        <Defs />
        <defs>
          <linearGradient id="rs-steel-up" x1="0" y1="0" x2="0" y2="1">{stop('0', 'color-mix(in srgb, var(--spark) 85%, var(--muted))')}{stop('1', 'color-mix(in srgb, var(--spark) 45%, var(--muted))')}</linearGradient>
          <linearGradient id="rs-steel-down" x1="0" y1="0" x2="0" y2="1">{stop('0', 'var(--muted)')}{stop('1', 'color-mix(in srgb, var(--muted) 60%, var(--anvil))')}</linearGradient>
          <linearGradient id="rs-gold" x1="0" y1="0" x2="0" y2="1">{stop('0', 'color-mix(in srgb, var(--rune-2) 80%, var(--spark))')}{stop('1', 'color-mix(in srgb, var(--rune-2) 55%, var(--anvil))')}</linearGradient>
          <linearGradient id="rs-rainbow" x1="0" y1="0" x2="1" y2="0">
            {RUNES.map((r, i) => <stop key={i} offset={`${(i / 5).toFixed(2)}`} style={{ stopColor: r.c }} />)}
          </linearGradient>
          <radialGradient id="rs-aura-fill">{stop('0', 'var(--arc-2)', 0.55)}{stop('0.55', 'var(--arc)', 0.22)}{stop('1', 'var(--arc)', 0)}</radialGradient>
          {RUNES.map((r, i) => <radialGradient key={i} id={`rs-pool-${i}`}>{stop('0', r.c, 0.9)}{stop('1', r.c, 0)}</radialGradient>)}
          <clipPath id="rs-blade"><path d={BLADE} /></clipPath>
          <clipPath id="rs-fuller"><path d={FULLER} /></clipPath>
        </defs>

        {/* the aura, behind everything, once all six runes are lit */}
        {done && <g className="rs-enchant"><ellipse className="rs-aura" cx="245" cy="150" rx="250" ry="86" /></g>}

        <g className="rs-sword">
          {/* pommel, grip, guard */}
          <path className="rs-gilt" d="M38 150 L48 137 L60 142 L60 158 L48 163 Z" />
          <circle className="rs-gem" cx="49" cy="150" r="4" />
          <rect className="rs-grip" x="60" y="144" width="56" height="12" rx="3" />
          <path className="rs-wraps" d={WRAPS} />
          <path className="rs-gilt" d={GUARD} />
          <circle className="rs-gem" cx="125" cy="150" r="5" />
          {/* the blade: two bevelled faces, the fuller, the edge */}
          <path fill="url(#rs-steel-up)" d="M136 129 H392 Q424 136 452 150 H136 Z" />
          <path fill="url(#rs-steel-down)" d="M136 150 H452 Q424 164 392 171 H136 Z" />
          <path className="rs-edge" d={BLADE} />
          <path className="rs-fuller" d={FULLER} />
          <g clipPath="url(#rs-fuller)"><rect className="rs-fuller-glow" x="148" y="140" width="242" height="20" fill="url(#rs-rainbow)" /></g>
          {/* the runes, each pooling its colour in the fuller when lit */}
          {RUNES.map((r, i) => (
            <g key={i} transform={`translate(${r.x} 150)`} style={{ '--c': r.c } as CSSProperties}>
              <ellipse className={i <= s ? 'rs-pool lit' : 'rs-pool'} rx="17" ry="9" fill={`url(#rs-pool-${i})`} />
              <path className={i < s ? 'rs-rune lit' : i === s ? 'rs-rune lit now' : 'rs-rune'} d={r.d} />
              {!calm && i === s && <circle className="rs-flare" r="13" />}
            </g>
          ))}
          {/* the awakening: light surges down the fuller; then a sheen keeps passing along the blade */}
          {done && !calm && <g clipPath="url(#rs-fuller)"><rect className="rs-surge" x="90" y="140" width="60" height="20" fill="url(#forge-sheen)" /></g>}
          {done && <g clipPath="url(#rs-blade)"><path className="rs-sheen" d="M100 120 L126 120 L112 180 L86 180 Z" fill="url(#forge-sheen)" /></g>}
        </g>

        {/* the enchantment: a shockwave as the sword awakens, then lightning running round it and motes circling it */}
        {done && !calm && <ellipse className="rs-shock" cx="245" cy="150" rx="220" ry="62" />}
        {done && (
          <g className="rs-enchant">
            <path className="rs-bolt" d={bolt(3, 245, 150, 214, 40)} pathLength={100} />
            <path className="rs-bolt b2" d={bolt(5, 245, 150, 222, 68)} pathLength={100} />
            {!calm && MOTES.map((m, i) => (
              <g key={i} className="rs-mote" style={{ '--rx': `${m.rx}px`, '--ry': `${m.ry}px`, '--t': `${m.t}s`, '--d': `${m.d}s` } as CSSProperties}>
                <circle className={`tone${m.tone}`} cx="245" cy={150 + m.dy} r={m.r} />
              </g>
            ))}
            <path className="rs-twinkle" d="M452 140 L454 148 L462 150 L454 152 L452 160 L450 152 L442 150 L450 148 Z" />
          </g>
        )}
      </svg>
      <RankTrack stage={s} said="An animation of a runic sword: six runes engraved along its blade light one after another, each in its own colour, one for each of the forge's six ranks; when all are lit the sword becomes a masterwork, enchanted, with a blue-violet aura, motes circling it and lightning running about it." />
    </div>
  )
}
