// The forge's six ranks as the gate animation shows them: each hammer strike turns the piece into the next one.
// The names match the server's rank ladder (Settings → Forge ranks only change the thresholds, never the names).
export const STAGES = [
  { rank: 'Ore', line: 'Raw from the earth' },
  { rank: 'Ingot', line: 'Smelted and cast' },
  { rank: 'Tempered', line: 'Heated, struck and hardened' },
  { rank: 'Blade', line: 'Drawn out to an edge' },
  { rank: 'Sword', line: 'Balanced and whole' },
  { rank: 'Masterwork', line: 'The forge’s finest' },
] as const

export const LAST = STAGES.length - 1

// Timing of one strike, in ms: the hammer falls at IMPACT into a CYCLE-long swing. After the Masterwork the forge
// rests for HOLD (the gleam), then the piece cools back to ore for RESET before the first strike again.
export const CYCLE = 1800
export const IMPACT = 1260
export const HOLD = 2800
export const RESET = 900

// RUNE_STEP is how long each rank lasts in the rune forge before the next rune lights (ms): long enough for the rune
// to wake, its beam to strike the centre and the piece to change. The masterwork has none: once the circle is
// complete the enchanted sword stays.
export const RUNE_STEP = [1500, 1700, 1700, 1700, 1700] as const

// next is what follows a stage in the loop: the next rank, and after the Masterwork the ore again.
export const next = (stage: number) => (stage >= LAST ? 0 : stage + 1)

// sparks gives each spark of a strike its flight: angles fan up and out from the point of impact, distances vary,
// so one burst never looks like a ring. Deterministic, so a render test sees the same markup every time.
export function sparks(n = 12): { dx: number; dy: number; r: number }[] {
  return Array.from({ length: n }, (_, i) => {
    const a = (-170 + (160 * i) / (n - 1)) * (Math.PI / 180) // from left-and-up, over the top, to right-and-up
    const d = 70 + ((i * 37) % 70)
    return { dx: Math.round(Math.cos(a) * d), dy: Math.round(Math.sin(a) * d), r: i % 3 === 0 ? 3.4 : 2.4 }
  })
}

// bolt is a closed, jagged loop around the rune forge's upright blade (an ellipse about 240,142 whose radius jumps in
// and out), for lightning that runs round the masterwork. `seed` picks the jitter; deterministic, so a render test sees
// the same markup every time.
export function bolt(seed: number): string {
  const n = 40
  const pts = Array.from({ length: n }, (_, i) => {
    const a = (i / n) * 2 * Math.PI
    const j = ((i * seed) % 7) - 3 // -3 … 3, so the loop zigzags in and out by up to ~9
    return `${(240 + (44 + j * 2.5) * Math.cos(a)).toFixed(1)} ${(142 + (84 + j * 3) * Math.sin(a)).toFixed(1)}`
  })
  return `M${pts.join(' L')} Z`
}
