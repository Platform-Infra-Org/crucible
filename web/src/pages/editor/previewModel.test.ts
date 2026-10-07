import { expect, test } from 'vitest'
import { grade, parseLab, parseQuiz } from './previewModel'

const quiz = `pass_threshold: 0.5
questions:
  - {id: s, type: single, prompt: Hot?, options: ["no", "yes"], answer: 1}
  - {id: m, type: multi, prompt: Which?, options: [a, b, c], answer: [0, 2]}
  - {id: e, type: exact, prompt: Port?, answer: "80"}
  - {id: r, type: regex, prompt: Version?, answer: '\\d+\\.\\d+'}
  - {id: o, type: order, prompt: Order?, options: [one, two]}
  - {id: x, type: match, prompt: Match?, pairs: [[a, "1"], [b, "2"]]}
  - {id: t, type: text, prompt: Explain, rubric: Mentions heat}
`

test('a quiz parses, with defaults', () => {
  const { quiz: q } = parseQuiz(quiz)
  expect(q?.passThreshold).toBe(0.5)
  expect(q?.questions.map((x) => x.points)).toEqual([1, 1, 1, 1, 1, 1, 1])
  expect(parseQuiz('questions: [').error).toBeTruthy()
  expect(parseQuiz('questions: []').quiz?.passThreshold).toBe(0.8)
})

test('answers are graded the way the server grades them', () => {
  const qs = parseQuiz(quiz).quiz!.questions
  expect(grade(qs[0], 1)).toBe(true)
  expect(grade(qs[0], 0)).toBe(false)
  expect(grade(qs[1], [2, 0])).toBe(true)
  expect(grade(qs[1], [0])).toBe(false)
  expect(grade(qs[1], [10, 2])).toBe(false) // numeric sort, not lexicographic
  expect(grade(qs[2], ' 80 ')).toBe(true)
  expect(grade(qs[3], '1.2')).toBe(true)
  expect(grade(qs[3], 'v1.2')).toBe(false)
  expect(grade(qs[4], ['one', 'two'])).toBe(true)
  expect(grade(qs[5], { a: '1', b: '2' })).toBe(true)
  expect(grade(qs[6], 'anything')).toBeUndefined()
  const cs = { ...qs[2], answer: 'Yes', caseSensitive: true }
  expect(grade(cs, 'yes')).toBe(false)
  expect(grade({ ...cs, caseSensitive: false }, 'YES')).toBe(true)
})

test('a lab lists tasks, kinds and effective hint costs', () => {
  const { lab } = parseLab(`id: l
runtime: local
hint_cost: 0.25
terminals: [{name: shell, service: shell}]
tasks:
  - {id: t1, instructions: tasks/1.md, check: {script: c.sh, run_in: shell}, points: 2, hints: [{text: Try echo}, {file: hints/h.md, cost: 1}]}
  - {id: t2, instructions: tasks/2.md, human_review: true, rubric: R}
`)
  expect(lab?.terminals).toEqual([{ name: 'shell', service: 'shell' }])
  expect(lab?.tasks.map((t) => t.kind)).toEqual(['check', 'review'])
  expect(lab?.tasks[0].hints.map((h) => h.cost)).toEqual([0.25, 1])
  expect(lab?.tasks[1].points).toBe(1)
})
