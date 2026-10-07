// Monaco is set up here only. This file is reachable only from the editor route's lazy chunk; scripts/check-chunks.mjs
// fails the build if Monaco reaches the main bundle. Workers come from Vite ?worker imports: same-origin files, so the
// CSP keeps script-src 'self' with no blob: and no new hosts.
import * as monaco from 'monaco-editor/editor/editor.api'
// Only what the editor uses (index.js would also bring TS/CSS/HTML/JSON language services and their ~9 MB of workers).
import 'monaco-editor/esm/vs/base/browser/ui/codicons/codicon/codicon.css' // via the vite.config.ts alias
import 'monaco-editor/esm/vs/base/browser/ui/codicons/codicon/codicon-modifiers.css'
import 'monaco-editor/editor/browser/coreCommands'
import 'monaco-editor/editor/browser/widget/diffEditor/diffEditor.contribution' // F7 / Shift+F7 step through the rebase diff
import 'monaco-editor/editor/common/standaloneStrings'
import 'monaco-editor/features/find/register'
import 'monaco-editor/editor/standalone/browser/quickAccess/standaloneCommandsQuickAccess'
import 'monaco-editor/editor/standalone/browser/quickAccess/standaloneGotoLineQuickAccess'
import 'monaco-editor/editor/standalone/browser/quickAccess/standaloneHelpQuickAccess'
import 'monaco-editor/editor/contrib/bracketMatching/browser/bracketMatching'
import 'monaco-editor/editor/contrib/clipboard/browser/clipboard'
import 'monaco-editor/editor/contrib/codeAction/browser/codeActionContributions'
import 'monaco-editor/editor/contrib/comment/browser/comment'
import 'monaco-editor/editor/contrib/contextmenu/browser/contextmenu'
import 'monaco-editor/editor/contrib/cursorUndo/browser/cursorUndo'
import 'monaco-editor/editor/contrib/folding/browser/folding'
import 'monaco-editor/editor/contrib/format/browser/formatActions'
import 'monaco-editor/editor/contrib/gotoError/browser/gotoError'
import 'monaco-editor/editor/contrib/gotoError/browser/markerSelectionStatus'
import 'monaco-editor/editor/contrib/hover/browser/hoverContribution'
import 'monaco-editor/editor/contrib/indentation/browser/indentation'
import 'monaco-editor/editor/contrib/linesOperations/browser/linesOperations'
import 'monaco-editor/editor/contrib/links/browser/links'
import 'monaco-editor/editor/contrib/multicursor/browser/multicursor'
import 'monaco-editor/editor/contrib/readOnlyMessage/browser/contribution'
import 'monaco-editor/editor/contrib/smartSelect/browser/smartSelect'
import 'monaco-editor/editor/contrib/snippet/browser/snippetController2'
import 'monaco-editor/editor/contrib/suggest/browser/suggestController'
import 'monaco-editor/editor/contrib/toggleTabFocusMode/browser/toggleTabFocusMode'
import 'monaco-editor/editor/contrib/unicodeHighlighter/browser/unicodeHighlighter'
import 'monaco-editor/editor/contrib/wordHighlighter/browser/wordHighlighter'
import 'monaco-editor/editor/contrib/wordOperations/browser/wordOperations'
import 'monaco-editor/editor/contrib/find/browser/findController'
import 'monaco-editor/languages/definitions/markdown/register'
import 'monaco-editor/languages/definitions/yaml/register'
import 'monaco-editor/languages/definitions/shell/register'
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
  // monaco-yaml 5.5 calls editor.createWebWorker({ label, createData }), the pre-0.55 API; Monaco 0.57 wants the Worker
  // itself, and would otherwise hand YAML requests to the plain editor worker. This is monaco-editor's own
  // internal/common/workers.js shim: an 'ignore' message, then createData, then the worker server.
  const createWebWorker = (o: { createData?: unknown; host?: Record<string, Function>; keepIdleModels?: boolean }) => {
    const worker = new YamlWorker()
    worker.postMessage('ignore')
    worker.postMessage(o.createData)
    return monaco.editor.createWebWorker({ worker, host: o.host, keepIdleModels: o.keepIdleModels })
  }
  configureMonacoYaml({ ...monaco, editor: { ...monaco.editor, createWebWorker } } as typeof monaco, {
    enableSchemaRequest: false, // schemas are inline; nothing is fetched
    schemas: (['training', 'module', 'quiz', 'lab'] as const).map((kind) => ({
      uri: `inmemory://crucible/${kind}.schema.json`,
      fileMatch: [`**/${kind}.yaml`],
      schema: schemas[kind],
    })),
  })
}

export { monaco }
