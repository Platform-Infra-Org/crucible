import { expect, test, type Page } from '@playwright/test'
import { execFileSync } from 'node:child_process'
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { login, setEditorText, watchCsp } from './helpers'

// Idempotent on a KEEP=1 stack: every name carries a run id; the reading renamed and the hint deleted are whichever
// exist now, and a fresh hint file is left for the next run. The other tests discard their drafts.
const run = Date.now().toString(36)

// startEdit opens a fresh draft of training in the editor and returns its id.
async function startEdit(page: Page, training: string): Promise<number> {
  await page.getByRole('link', { name: 'Edits', exact: true }).click()
  await page.getByRole('combobox', { name: 'Training' }).selectOption(training) // not the Hearth's "Training forged" bar
  await page.getByRole('button', { name: 'Start an edit' }).click()
  await expect(page.getByRole('region', { name: 'Explorer' })).toBeVisible()
  return Number(/\/edits\/drafts\/(\d+)/.exec(page.url())![1])
}

// savedOps is the draft as the server has it: what autosave wrote.
const savedOps = async (page: Page, id: number) => JSON.stringify((await (await page.request.get(`/api/authoring/drafts/${id}`)).json()).ops)

// pushUpstream commits a change to a seeded content repo, as a maintainer would in git, and returns the new head. The
// API picks it up on its next sync (10 s in compose).
function pushUpstream(training: string, file: string, change: (text: string) => string): string {
  const bare = resolve(__dirname, '../../.local/git', `${training}.git`)
  const work = mkdtempSync(join(tmpdir(), 'crucible-e2e-'))
  const git = (...args: string[]) => execFileSync('git', ['-C', work, '-c', 'user.name=crucible', '-c', 'user.email=crucible@local', ...args], { encoding: 'utf8' }).trim()
  try {
    execFileSync('git', ['clone', '-q', bare, work])
    writeFileSync(join(work, file), change(readFileSync(join(work, file), 'utf8')))
    git('commit', '-qam', `upstream change ${run}`)
    git('push', '-q', 'origin', 'HEAD:main')
    execFileSync('chmod', ['-R', 'a+rwX', bare]) // the api container (another uid) pushes merges here too
    return git('rev-parse', 'HEAD')
  } finally {
    rmSync(work, { recursive: true, force: true })
  }
}

test('a leader shapes Forge 103 in the editor, a maintainer merges it, the trainee reads it', async ({ browser }) => {
  const leader = await login(browser, 'leader')
  const csp = await watchCsp(leader)
  const workers: string[] = []
  leader.on('worker', (w) => workers.push(w.url()))
  const files: string[] = (await (await leader.request.get('/api/content/forge-103/files')).json()).files.map((f: { path: string }) => f.path)
  const scroll = files.find((p) => /^modules\/01-anvil\/reading\/scroll[^/]*\.md$/.test(p))!
  const hint = files.find((p) => /^modules\/01-anvil\/hints\/h-[^/]*\.md$/.test(p))!
  const renamed = `modules/01-anvil/reading/scroll-${run}.md`
  const mod = `02-m${run}`

  await startEdit(leader, 'forge-103')
  await leader.getByLabel('Title').fill(`Shape the smithy ${run}`)

  // Blocks: a module, then a quiz in it. The quiz opens with the insertion selected; the preview marks the answer.
  await leader.getByRole('button', { name: 'Blocks', exact: true }).click()
  await leader.getByRole('button', { name: 'Module with a reading', exact: true }).click()
  await leader.getByLabel(/^id/).fill(mod)
  await leader.getByLabel(/^title/).fill(`Embers ${run}`)
  await leader.getByRole('button', { name: 'Add to the draft' }).click()
  await expect(leader.getByRole('treeitem', { name: `modules/${mod}/module.yaml`, exact: true })).toBeVisible()
  await leader.getByRole('button', { name: 'Blocks', exact: true }).click()
  await leader.getByRole('button', { name: 'Quiz file', exact: true }).click()
  await leader.getByLabel(/^module/).selectOption(mod)
  await leader.getByLabel(/^prompt/).fill('Which iron do you strike?')
  await leader.getByLabel(/^options/).fill('Cold iron\nHot iron')
  await leader.getByLabel(/^answer/).fill('1')
  await leader.getByRole('button', { name: 'Add to the draft' }).click()
  await expect(leader.getByTestId('edit-preview')).toContainText('Which iron do you strike?')
  await expect(leader.getByTestId('edit-preview')).toContainText('✓ right answer')

  // Rename the reading: module.yaml now names a missing file. Jumping to the problem opens module.yaml (its text
  // arrives late on purpose) on the problem's line, squiggled; the leader fixes the item.
  await leader.getByRole('button', { name: 'Explorer', exact: true }).click()
  await leader.getByRole('treeitem', { name: scroll, exact: true }).click({ button: 'right' }) // the explorer's context menu
  await leader.getByRole('menuitem', { name: 'Rename' }).click()
  await leader.getByLabel(`New path for ${scroll}`).fill(renamed)
  await leader.getByLabel(`New path for ${scroll}`).press('Enter')
  await leader.route(/\/file\?path=modules%2F01-anvil%2Fmodule\.yaml$/, async (r) => { await new Promise((ok) => setTimeout(ok, 1500)); await r.continue() })
  await leader.getByRole('button', { name: /^Problems \([1-9]/ }).click()
  await leader.getByRole('button', { name: /modules\/01-anvil\/module\.yaml:3/ }).click()
  await expect(leader.getByTestId('code-editor').locator('.active-line-number')).toHaveText('3')
  await expect(leader.locator('.squiggly-error').first()).toBeVisible()
  const moduleYaml: string = (await (await leader.request.get('/api/content/forge-103/file?path=modules/01-anvil/module.yaml')).json()).content
  await setEditorText(leader, moduleYaml.replace(scroll.replace('modules/01-anvil/', ''), renamed.replace('modules/01-anvil/', '')))
  await expect(leader.getByRole('button', { name: 'Problems (0)' })).toBeVisible()

  // Ctrl/Cmd+P from inside the editor opens one Go to file; Enter opens the match.
  await leader.keyboard.press('ControlOrMeta+P')
  await expect(leader.getByRole('dialog', { name: 'Go to file' })).toHaveCount(1)
  await leader.getByLabel('File name').fill(`scroll-${run}`)
  await leader.keyboard.press('Enter')
  await expect(leader.getByRole('dialog', { name: 'Go to file' })).toHaveCount(0)
  await expect(leader.getByTestId('edit-preview')).toContainText('Every smith keeps a scroll')

  // Delete the spare hint and leave a fresh one for the next run.
  await leader.getByRole('button', { name: 'Explorer', exact: true }).click()
  await leader.getByRole('treeitem', { name: hint, exact: true }).click({ button: 'right' })
  await leader.getByRole('menuitem', { name: 'New file' }).click()
  await expect(leader.getByLabel('Path of the new file')).toHaveValue('modules/01-anvil/hints/') // in the file's folder
  await leader.getByLabel('Path of the new file').fill(`modules/01-anvil/hints/h-${run}.md`)
  await leader.getByLabel('Path of the new file').press('Enter')
  await setEditorText(leader, `Spare hint from run ${run}.\n`) // not the old text, or git shows a rename, not a delete
  await leader.getByRole('treeitem', { name: hint, exact: true }).focus()
  await leader.keyboard.press('Delete') // the confirm dialog is accepted by login()
  await expect(leader.getByRole('treeitem', { name: hint, exact: true })).toHaveCount(0)

  await leader.getByRole('button', { name: /^Changes \(/ }).click()
  await expect(leader.getByRole('list', { name: 'Changes' })).toContainText('Renamed')
  await expect(leader.getByRole('list', { name: 'Changes' })).toContainText('Deleted')
  await leader.getByRole('button', { name: 'Submit for review' }).click()
  await expect(leader.getByTestId('edit-status')).toHaveText('pending')
  await expect(leader.getByTestId('diff')).toContainText(`rename to ${renamed}`)
  await expect(leader.getByTestId('diff')).toContainText('deleted file mode')

  expect(workers.length, 'Monaco and the YAML worker started').toBeGreaterThan(0)
  expect(workers.every((u) => u.startsWith('http://localhost:8080/')), workers.join(' ')).toBe(true)
  expect(csp, csp.join('\n')).toEqual([])

  const senior = await login(browser, 'senior')
  await senior.getByRole('link', { name: 'Edits', exact: true }).click()
  await senior.getByRole('link', { name: `Shape the smithy ${run}` }).click()
  await senior.getByRole('button', { name: 'Approve and merge' }).click()
  await expect(senior.getByTestId('edit-status')).toHaveText('merged', { timeout: 30_000 })

  const trainee = await login(browser, 'trainee')
  await trainee.getByRole('link', { name: /Forge 103/ }).click()
  await expect(trainee.getByRole('heading', { name: `Embers ${run}` })).toBeVisible({ timeout: 30_000 })
})

test('a block lands in the open file and autosaves; Ctrl/Cmd+Z takes it out; a YAML hover links to the Docs', async ({ browser }) => {
  const leader = await login(browser, 'leader')
  const csp = await watchCsp(leader)
  const id = await startEdit(leader, 'forge-103')
  const editor = leader.getByTestId('code-editor')

  // A quiz in 01-anvil opens its quiz.yaml; a question block then appends to that open file.
  await leader.getByRole('button', { name: 'Blocks', exact: true }).click()
  await leader.getByRole('button', { name: 'Quiz file', exact: true }).click()
  await leader.getByLabel(/^module/).selectOption('01-anvil')
  await leader.getByLabel(/^prompt/).fill('Which iron do you strike?')
  await leader.getByLabel(/^options/).fill('Cold iron\nHot iron')
  await leader.getByLabel(/^answer/).fill('1')
  await leader.getByRole('button', { name: 'Add to the draft' }).click()
  await expect(editor).toContainText('Which iron do you strike?')
  await leader.getByRole('button', { name: 'Blocks', exact: true }).click()
  await leader.getByRole('button', { name: 'Single choice', exact: true }).click()
  await leader.getByLabel(/^module/).selectOption('01-anvil')
  await leader.getByLabel(/^id/).fill(`q-${run}`)
  await leader.getByLabel(/^prompt/).fill(`Strike now ${run}?`)
  await leader.getByLabel(/^options/).fill('Yes\nNo')
  await leader.getByLabel(/^answer/).fill('0')
  await leader.getByRole('button', { name: 'Add to the draft' }).click()
  await expect(editor).toContainText(`Strike now ${run}?`)
  await expect.poll(() => savedOps(leader, id), { timeout: 20_000 }).toContain(`Strike now ${run}?`)

  // The insert is one edit in the open file: undo takes it out, and the draft saves without it.
  await leader.keyboard.press('ControlOrMeta+Z')
  await expect(editor).not.toContainText(`Strike now ${run}?`)
  await expect(editor).toContainText('Which iron do you strike?')
  await expect.poll(() => savedOps(leader, id), { timeout: 20_000 }).not.toContain(`Strike now ${run}?`)

  // Hovering a key shows what it means and links to the reference, which opens in a new tab.
  await editor.locator('.view-line', { hasText: 'pass_threshold' }).hover({ position: { x: 20, y: 5 } })
  const link = leader.locator('.monaco-hover').getByRole('link', { name: 'Building blocks: every key' })
  await expect(link).toBeVisible()
  const [docs] = await Promise.all([leader.context().waitForEvent('page'), link.click()])
  await expect(docs).toHaveURL(/\/docs\/authors\/building-blocks$/)
  await expect(docs.getByRole('heading', { name: 'module.yaml' })).toBeVisible()
  expect(csp, csp.join('\n')).toEqual([])

  await leader.getByRole('button', { name: 'Discard draft' }).click() // confirm accepted by login()
  await expect(leader).toHaveURL(/\/edits$/)
  expect((await leader.request.get(`/api/authoring/drafts/${id}`)).status()).toBe(404)
})

test('newer content: a file changed on both sides is resolved, and the draft ends on the new base', async ({ browser }) => {
  const leader = await login(browser, 'leader')
  const csp = await watchCsp(leader)
  const id = await startEdit(leader, 'forge-103')
  const described = (who: string) => (text: string) => text.replace(/^description: .*$/m, `description: ${who} ${run}`)

  await leader.getByRole('treeitem', { name: 'training.yaml', exact: true }).click()
  const original: string = (await (await leader.request.get('/api/content/forge-103/file?path=training.yaml')).json()).content
  await setEditorText(leader, described('Mine')(original))
  await expect.poll(() => savedOps(leader, id), { timeout: 20_000 }).toContain(`Mine ${run}`)

  const head = pushUpstream('forge-103', 'training.yaml', described('Upstream'))
  const rebase = leader.getByRole('button', { name: 'Newer content: rebase draft' })
  await expect(async () => { // the API syncs every 10 s; the editor learns of the new head when it loads the draft
    await leader.reload()
    await expect(rebase).toBeVisible({ timeout: 2_000 })
  }).toPass({ timeout: 60_000 })
  await rebase.click()
  const panel = leader.getByRole('region', { name: 'Resolve newer content' })
  await expect(panel).toContainText('training.yaml')
  await expect(panel).toContainText('Changed both here and upstream.')
  await expect(panel.getByTestId('rebase-diff')).toContainText(`Upstream ${run}`)
  await panel.getByRole('button', { name: 'Use the right side' }).click()

  await expect(leader.getByText('Rebased onto the newest content.')).toBeVisible()
  await expect(leader.getByRole('status').filter({ hasText: `base ${head.slice(0, 7)}` })).toBeVisible()
  await expect(rebase).toHaveCount(0)
  const draft = await (await leader.request.get(`/api/authoring/drafts/${id}`)).json()
  expect(draft.base_sha).toBe(head)
  expect(JSON.stringify(draft.ops)).toContain(`Mine ${run}`)
  expect(csp, csp.join('\n')).toEqual([])

  // Discard draft, from the editor, removes it.
  await leader.getByRole('button', { name: 'Discard draft' }).click() // confirm accepted by login()
  await expect(leader).toHaveURL(/\/edits$/)
  expect((await leader.request.get(`/api/authoring/drafts/${id}`)).status()).toBe(404)
})

// Review I-1: leaving through a link inside the app, within the autosave pause, still saves the latest change.
test('leaving the editor through a link saves the latest change first', async ({ browser }) => {
  const leader = await login(browser, 'leader')
  const id = await startEdit(leader, 'forge-103')
  await leader.getByRole('treeitem', { name: 'training.yaml', exact: true }).click()
  const original: string = (await (await leader.request.get('/api/content/forge-103/file?path=training.yaml')).json()).content
  await setEditorText(leader, original.replace(/^description: .*$/m, `description: Left ${run}`))
  await expect(leader.getByRole('status').filter({ hasText: 'Unsaved changes' })).toBeVisible()
  await leader.getByRole('link', { name: 'All edits' }).click() // well inside the 2 s autosave pause
  await expect(leader).toHaveURL(/\/edits$/)
  await expect.poll(() => savedOps(leader, id), { timeout: 10_000 }).toContain(`Left ${run}`)

  await leader.goto(`/edits/drafts/${id}`)
  await leader.getByRole('button', { name: 'Discard draft' }).click() // confirm accepted by login()
  await expect(leader).toHaveURL(/\/edits$/)
})
