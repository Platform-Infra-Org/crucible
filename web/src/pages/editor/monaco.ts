// Monaco is set up here only. This file is reachable only from the editor route's lazy chunk; scripts/check-chunks.mjs
// fails the build if Monaco reaches the main bundle. Workers come from Vite ?worker imports: same-origin files, so the
// CSP keeps script-src 'self' with no blob: and no new hosts.
import * as monaco from 'monaco-editor'
import { configureMonacoYaml } from 'monaco-yaml'
import EditorWorker from 'monaco-editor/editor/editor.worker?worker'
import YamlWorker from './yaml.worker?worker'

;(self as unknown as { MonacoEnvironment: monaco.Environment }).MonacoEnvironment = {
  getWorker: (_id: string, label: string) => (label === 'yaml' ? new YamlWorker() : new EditorWorker()),
}

let yamlReady = false

// setupYaml gives YAML files autocomplete, enum suggestions and hover docs from the generated schemas (once per page).
export function setupYaml(schemas: Record<string, object>) {
  if (yamlReady) return
  yamlReady = true
  configureMonacoYaml(monaco, {
    enableSchemaRequest: false, // schemas are inline; nothing is fetched
    schemas: (['training', 'module', 'quiz', 'lab'] as const).map((kind) => ({
      uri: `inmemory://crucible/${kind}.schema.json`,
      fileMatch: [`**/${kind}.yaml`],
      schema: schemas[kind],
    })),
  })
}

export { monaco }
