import type { QuizResult } from '../types'

// score/max are this attempt; percent/passed/status are quiz-wide (best instant + scored human answers).
// An earlier passing attempt must not make a failing one look like a win.
export const attemptPassed = (r: QuizResult, threshold: number) => r.max <= 0 || r.score / r.max >= threshold - 1e-9

export function resultMessage(r: QuizResult, threshold: number): string {
  const pct = Math.round(r.percent * 100)
  const attemptPct = r.max > 0 ? Math.round((r.score / r.max) * 100) : 100
  if (r.status === 'pending_review') return 'Submitted: a scorer still has to read your answers.'
  if (r.status === 'complete') {
    return attemptPassed(r, threshold) ? `Passed: ${pct}%. Tempered!` : `Not yet: this attempt scored ${attemptPct}%. An earlier attempt already passed.`
  }
  if (!r.pending_human) return `Not yet: ${pct}%. Reheat and try again.`
  return attemptPassed(r, threshold)
    ? 'Choices scored. Answer the questions a person scores below.'
    : `Not yet: your choices scored ${attemptPct}%. Reheat and try again.`
}
