export const usd = (n: number) => `$${n.toFixed(2)}`
/** A per-hour price: four decimals below a dollar, where list prices live ($0.0116). */
export const usdRate = (n: number) => `$${n.toFixed(n > 0 && n < 1 ? 4 : 2)}`

export function hours(seconds: number): string {
  const h = Math.floor(seconds / 3600)
  const m = Math.round((seconds % 3600) / 60)
  return [h ? `${h}h` : '', m ? `${m}m` : ''].filter(Boolean).join(' ') || '0m'
}
