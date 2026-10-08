// Build test (spec: "a build test asserts the main chunk does not include it"): Monaco may only be in lazily loaded
// chunks. Walks dist/index.html's entry script and everything it imports statically; dynamic import() is not followed.
import { readdirSync, readFileSync } from 'node:fs'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'

const dist = join(dirname(fileURLToPath(import.meta.url)), '..', 'dist')
const marker = 'MonacoEnvironment' // read by Monaco itself and set by pages/editor/monaco.ts; survives minification
const entry = /<script type="module"[^>]*src="\/([^"]+)"/.exec(readFileSync(join(dist, 'index.html'), 'utf8'))?.[1]
if (!entry) throw new Error('check-chunks: no entry script in dist/index.html')
const seen = new Set()
const walk = (file) => {
  if (seen.has(file)) return
  seen.add(file)
  const src = readFileSync(join(dist, file), 'utf8')
  if (src.includes(marker)) throw new Error(`check-chunks: Monaco reached the main bundle (${file}); import it only from pages/editor`)
  for (const m of src.matchAll(/(?:import|from)\s*["']\.\/([^"']+\.js)["']/g)) walk(join(dirname(file), m[1]))
}
walk(entry)
const lazy = readdirSync(join(dist, 'assets')).filter((f) => f.endsWith('.js') && readFileSync(join(dist, 'assets', f), 'utf8').includes(marker))
if (lazy.length === 0) throw new Error('check-chunks: no chunk contains Monaco at all, so this check proves nothing; update the marker')
console.log(`check-chunks: main bundle is ${seen.size} files without Monaco; Monaco is in ${lazy.join(', ')}`)
