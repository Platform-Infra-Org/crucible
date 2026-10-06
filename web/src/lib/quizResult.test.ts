import { describe, expect, it } from 'vitest'
import type { QuizResult } from '../types'
import { resultMessage } from './quizResult'

const r = (o: Partial<QuizResult>): QuizResult =>
  ({ score: 0, max: 1, percent: 0, passed: false, correct: {}, pending_human: false, status: 'in_progress', ...o })

describe('quiz result message', () => {
  it('says "Not yet" for a failed instant part even when people still score the rest', () => {
    expect(resultMessage(r({ score: 0, max: 2, pending_human: true }), 0.6)).toBe('Not yet: your choices scored 0%. Reheat and try again.')
  })
  it('sends a passing instant part on to the human questions', () => {
    expect(resultMessage(r({ score: 2, max: 2, percent: 0.2, pending_human: true }), 0.6)).toMatch(/^Choices scored/)
  })
  it('never calls a failing attempt a pass', () => {
    expect(resultMessage(r({ score: 0, max: 2, percent: 1, status: 'complete' }), 0.6)).toMatch(/^Not yet: this attempt scored 0%/)
    expect(resultMessage(r({ score: 2, max: 2, percent: 1, status: 'complete' }), 0.6)).toBe('Passed: 100%. Tempered!')
  })
})
