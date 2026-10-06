// Mirrors the server's limits (internal/scoring: 5 files, 20 MiB each) so an oversized upload fails here, with a clear reason.
export const maxFiles = 5
export const maxFileBytes = 20 << 20

export function uploadProblem(files: { name: string; size: number }[]): string | null {
  if (files.length > maxFiles) return `At most ${maxFiles} files, please.`
  const big = files.find((f) => f.size > maxFileBytes)
  return big ? `${big.name} is larger than 20 MiB.` : null
}
