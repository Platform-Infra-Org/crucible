import { useState } from 'react'
import { Markdown } from '../../components/Markdown'
import { grade, parseLab, parseQuiz, type QuestionView } from './previewModel'

const dir = (p: string) => p.slice(0, p.lastIndexOf('/') + 1)

// Preview renders the open file the way trainees will see it. Nothing executes: no checks, no labs. It works only on
// the draft's own text (the editor is reachable only by people allowed to see answer keys); it fetches nothing itself.
export function Preview({ path, text, read }: { path: string; text: string; read: (p: string) => string | undefined }) {
  if (path.endsWith('.md')) return <Markdown text={text} />
  if (path.endsWith('/quiz.yaml')) return <QuizPreview text={text} />
  if (path.endsWith('/lab.yaml')) return <LabPreview text={text} labDir={dir(path)} read={read} />
  return <pre>{text}</pre>
}

function QuizPreview({ text }: { text: string }) {
  const { quiz, error } = parseQuiz(text)
  if (!quiz) return <p role="alert" className="error">Can't preview: {error}</p>
  return (
    <div className="quiz-preview">
      <p className="muted">Pass at {Math.round(quiz.passThreshold * 100)}%. Right answers are marked: only editors see this.</p>
      {quiz.questions.map((q, i) => <QuestionPreview key={i} q={q} />)}
    </div>
  )
}

function QuestionPreview({ q }: { q: QuestionView }) {
  const [resp, setResp] = useState<unknown>(q.type === 'multi' ? [] : '')
  const [checked, setChecked] = useState<boolean>()
  const right = (i: number) => (q.type === 'single' ? q.answer === i : q.type === 'multi' && Array.isArray(q.answer) && q.answer.includes(i))
  return (
    <fieldset>
      <legend>{`${q.id} · ${q.type} · ${q.points} pt`}</legend>
      <Markdown text={q.prompt} />
      {(q.type === 'single' || q.type === 'multi') && q.options.map((o, i) => (
        <label key={i} style={{ display: 'block' }}>
          <input type={q.type === 'single' ? 'radio' : 'checkbox'} name={q.id}
            onChange={(e) => setResp(q.type === 'single' ? i : e.target.checked ? [...(resp as number[]), i] : (resp as number[]).filter((x) => x !== i))} />
          <span>{o}</span>{right(i) && <span className="pass"> ✓ right answer</span>}
        </label>
      ))}
      {(q.type === 'exact' || q.type === 'regex') && (
        <>
          <input aria-label={`Answer to ${q.id}`} value={String(resp)} onChange={(e) => setResp(e.target.value)} />
          <span className="pass"> ✓ {q.type === 'exact' ? `"${String(q.answer)}"` : `matches /${String(q.answer)}/`}</span>
        </>
      )}
      {q.type === 'order' && <p className="pass">✓ Right order: {q.options.join(' → ')} (trainees see them shuffled)</p>}
      {q.type === 'match' && <p className="pass">✓ Right pairs: {q.pairs.map((p) => p.join(' = ')).join(', ')}</p>}
      {q.type === 'terminal' && <p className="muted">Answered in the lab: a task with quiz: {q.id} runs its check.</p>}
      {['text', 'upload', 'signoff'].includes(q.type) && <p className="muted">Scored by a person on the Anvil.{q.rubric && ` Rubric: ${q.rubric}`}</p>}
      {['single', 'multi', 'exact', 'regex'].includes(q.type) && (
        <p><button className="ghost" onClick={() => setChecked(grade(q, resp))}>Try it</button>{' '}
          {checked !== undefined && <span role="status" className={checked ? 'pass' : 'error'}>{checked ? 'Right' : 'Not right'}</span>}</p>
      )}
    </fieldset>
  )
}

function LabPreview({ text, labDir, read }: { text: string; labDir: string; read: (p: string) => string | undefined }) {
  const { lab, error } = parseLab(text)
  if (!lab) return <p role="alert" className="error">Can't preview: {error}</p>
  return (
    <div className="lab-preview">
      <p className="muted">{lab.runtime} lab · terminals: {lab.terminals.map((t) => `${t.name} (${t.service})`).join(', ')}. Nothing runs in the preview; use crucible preview to try it.</p>
      <ol>
        {lab.tasks.map((t, n) => {
          const body = read(labDir + t.instructions)
          return (
            <li key={n}>
              <strong>{t.id}</strong> · {t.points} pt · {t.kind === 'review' ? 'scored by a person' : t.kind === 'quiz' ? 'answered by a terminal question' : 'checked by a script'}
              {body !== undefined ? <Markdown text={body} /> : <p className="muted">{t.instructions}</p>}
              {t.hints.length > 0 && <ul>{t.hints.map((h, i) => <li key={i}>{`Hint ${i + 1}: ${h.label} (costs ${h.cost} pt)`}</li>)}</ul>}
            </li>
          )
        })}
      </ol>
    </div>
  )
}
