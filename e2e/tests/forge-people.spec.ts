import { expect, test, type Browser, type Page } from '@playwright/test'
import { contentEdits, enroll, setEditorText } from './helpers'

async function login(browser: Browser, user: string): Promise<Page> {
  const page = await (await browser.newContext()).newPage()
  page.on('dialog', (d) => d.accept())
  await page.goto('/')
  await page.getByRole('button', { name: 'Enter the forge' }).click() // the gate, then the identity provider
  await page.locator('#username').fill(user)
  await page.locator('#password').fill(user)
  await page.locator('#kc-login').click()
  await expect(page.getByRole('heading', { name: 'Hearth' })).toBeVisible()
  return page
}

// Idempotent on a KEEP=1 stack: titles and the appended line carry a run id, and enroll tolerates an existing program.
const run = Date.now().toString(36)

test('a content edit is reviewed and merged; the trainee earns a badge; mentor and leader see the journey', async ({ browser }) => {
  // The leader starts Forge 102 for the team and enrols the trainee (a no-op on a KEEP=1 rerun).
  const leader = await login(browser, 'leader')
  await enroll(leader, 'forge-102', 'trainee@crucible.local')

  // The leader proposes an edit; authors never approve their own.
  await (await contentEdits(leader, 'forge-102')).getByRole('link', { name: 'Edit content' }).click()
  await leader.getByRole('treeitem', { name: 'modules/01-sparks/reading/sparks.md', exact: true }).click()
  const sparks = await (await leader.request.get('/api/content/forge-102/file?path=modules/01-sparks/reading/sparks.md')).json()
  expect(sparks.content).toMatch(/Every blade starts as a spark/)
  await setEditorText(leader, sparks.content + `\nThe anvil remembers ${run}.\n`)
  await expect(leader.getByTestId('edit-preview')).toContainText(`The anvil remembers ${run}.`)
  await leader.getByLabel('Title').fill(`Add a line about the anvil ${run}`)
  await leader.getByRole('button', { name: 'Submit for review' }).click()
  await expect(leader.getByRole('heading', { name: `Add a line about the anvil ${run}` })).toBeVisible()
  await expect(leader.getByTestId('edit-status')).toHaveText('pending')
  await expect(leader.getByRole('button', { name: 'Approve and merge' })).toHaveCount(0)

  // The senior (a maintainer of Forge 102) reviews the diff and merges.
  const senior = await login(browser, 'senior')
  await (await contentEdits(senior, 'forge-102')).getByRole('link', { name: `Add a line about the anvil ${run}` }).click()
  await expect(senior.getByTestId('diff')).toContainText(`+The anvil remembers ${run}.`)
  await senior.getByLabel('Review note').fill('Lovely.')
  await senior.getByRole('button', { name: 'Approve and merge' }).click()
  await expect(senior.getByTestId('edit-status')).toHaveText('merged', { timeout: 30_000 })

  // The trainee reads the merged text, passes the quiz, and earns the badge.
  const trainee = await login(browser, 'trainee')
  await trainee.getByRole('link', { name: /Forge 102/ }).click()
  await trainee.getByRole('link', { name: 'A Spark' }).click()
  await expect(trainee.getByText(`The anvil remembers ${run}.`)).toBeVisible()
  await expect(trainee.getByRole('progressbar', { name: 'Reading progress' })).toBeVisible()
  await trainee.getByRole('button', { name: 'Mark as read' }).click()
  await trainee.getByTestId('module-01-sparks').getByRole('link', { name: 'Quiz' }).click()
  await trainee.getByLabel('Hot iron').check()
  await trainee.getByRole('button', { name: 'Submit answers' }).click()
  await expect(trainee.getByRole('status').filter({ hasText: 'Passed' })).toBeVisible()
  await trainee.getByRole('link', { name: 'Hearth', exact: true }).click()
  await expect(trainee.getByTestId('badges')).toContainText('Forge 102: Sparks')
  // CARRY (M4): Forge 101's cluster module can't be finished in local-check, so never assert a particular rank.
  await expect(trainee.getByTestId('rank')).toHaveText(/^(Ore|Ingot|Tempered|Blade|Sword|Masterwork)$/)

  // The trainee's mentor (the senior) and the team leader see Forge 102 forged.
  await senior.getByRole('link', { name: 'Mentor', exact: true }).click()
  await expect(senior.getByTestId('mentee-trainee@crucible.local').getByTestId('journey-trainee@crucible.local-forge-102').getByRole('listitem').filter({ hasText: 'Sparks' })).toContainText(/Sparks\s*:\s*forged/)
  // The leader reaches the team from their user card: their icon, name, email and teams.
  await leader.getByRole('button', { name: /Leo Leader/ }).click()
  const card = leader.getByRole('dialog', { name: 'Your user card' })
  await expect(card).toContainText('leader@crucible.local')
  await card.getByRole('button', { name: 'Anvil' }).click()
  await expect(card.getByRole('button', { name: 'Anvil' })).toHaveAttribute('aria-pressed', 'true')
  await card.getByRole('link', { name: 'The Forge' }).click()
  await expect(leader.getByRole('heading', { name: 'The Forge' })).toBeVisible()
  await leader.getByRole('link', { name: 'Journey', exact: true }).click()
  await expect(leader.getByTestId('journey-trainee@crucible.local-forge-102').getByRole('listitem').filter({ hasText: 'Sparks' })).toContainText(/Sparks\s*:\s*forged/)

  // The catalog lists Forge 102 with its estimate, and the trainee's labs page answers.
  await trainee.getByRole('link', { name: 'Trainings', exact: true }).click()
  await expect(trainee.getByRole('heading', { name: 'Forge 102: Sparks' })).toBeVisible()
  await expect(trainee.getByTestId('catalog-forge-102')).toContainText('≈ 0.25 h')
  await trainee.getByRole('link', { name: 'Labs', exact: true }).click()
  await expect(trainee.getByRole('heading', { name: 'Labs' })).toBeVisible()
})
