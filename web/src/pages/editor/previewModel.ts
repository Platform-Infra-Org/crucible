import { parse } from 'yaml'

export type QuestionView = { id: string; type: string; prompt: string; options: string[]; pairs: string[][]; answer: unknown; caseSensitive: boolean; points: number; rubric?: string }
export type QuizView = { passThreshold: number; questions: QuestionView[] }
export type TaskView = { id: string; instructions: string; points: number; kind: 'check' | 'quiz' | 'review'; hints: { label: string; cost: number }[] }
export type LabView = { runtime: string; terminals: { name: string; service: string }[]; tasks: TaskView[] }

type Raw = Record<string, unknown>
const read = (text: string): { doc?: Raw; error?: string } => {
  try { return { doc: (parse(text) ?? {}) as Raw } } catch (e) { return { error: (e as Error).message } }
}
const arr = <T,>(v: unknown): T[] => (Array.isArray(v) ? (v as T[]) : [])

export function parseQuiz(text: string): { quiz?: QuizView; error?: string } {
  const { doc, error } = read(text)
  if (!doc) return { error }
  return {
    quiz: {
      passThreshold: Number(doc.pass_threshold) || 0.8, // 0 or unset means 0.8, as the loader does
      questions: arr<Raw>(doc.questions).map((q) => ({
        id: String(q.id ?? ''), type: String(q.type ?? ''), prompt: String(q.prompt ?? ''), options: arr<unknown>(q.options).map(String),
        pairs: arr<unknown[]>(q.pairs).map((p) => arr<unknown>(p).map(String)), answer: q.answer, caseSensitive: q.case_sensitive === true,
        points: Number(q.points ?? 1) || 1, rubric: q.rubric === undefined ? undefined : String(q.rubric),
      })),
    },
  }
}

// grade mirrors correct() in internal/learn/quiz.go for the instantly scored types; undefined = scored in the lab or by a person.
export function grade(q: QuestionView, response: unknown): boolean | undefined {
  const norm = (s: unknown) => (q.caseSensitive ? String(s).trim() : String(s).trim().toLowerCase())
  const nums = (v: unknown) => [...new Set(arr<number>(v))].sort((a, b) => a - b).join()
  switch (q.type) {
    case 'single': return response === q.answer
    case 'multi': return nums(response) === nums(q.answer)
    case 'exact': return norm(response) === norm(q.answer)
    case 'regex': try { return new RegExp(`^(?:${String(q.answer)})$`, q.caseSensitive ? '' : 'i').test(String(response).trim()) } catch { return false }
    case 'order': return JSON.stringify(response) === JSON.stringify(q.options)
    case 'match': return q.pairs.every(([l, r]) => (response as Record<string, string>)?.[l] === r)
  }
  return undefined
}

export function parseLab(text: string): { lab?: LabView; error?: string } {
  const { doc, error } = read(text)
  if (!doc) return { error }
  const hintCost = Number(doc.hint_cost ?? 0)
  return {
    lab: {
      runtime: String(doc.runtime ?? ''),
      terminals: arr<Raw>(doc.terminals).map((t) => ({ name: String(t.name ?? ''), service: String(t.service ?? '') })),
      tasks: arr<Raw>(doc.tasks).map((t) => ({
        id: String(t.id ?? ''), instructions: String(t.instructions ?? ''), points: Number(t.points ?? 1) || 1,
        kind: t.human_review === true ? 'review' : t.quiz ? 'quiz' : 'check',
        hints: arr<Raw>(t.hints).map((h) => ({ label: h.file ? `File ${String(h.file)}` : String(h.text ?? ''), cost: h.cost === undefined ? hintCost : Number(h.cost) })),
      })),
    },
  }
}
