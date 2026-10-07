import { Markdown } from '../../components/Markdown'

// Preview renders the open file the way trainees will see it. Nothing executes.
export function Preview({ path, text }: { path: string; text: string; read: (p: string) => string | undefined }) {
  if (path.endsWith('.md')) return <Markdown text={text} />
  return <pre>{text}</pre>
}
