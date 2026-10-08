import { expect, test, type Browser, type Page } from '@playwright/test'
import { spawn, type ChildProcess } from 'node:child_process'
import { enroll } from './helpers'

let agent: ChildProcess | undefined
test.afterAll(() => {
  agent?.kill('SIGTERM') // the agent tears down its compose projects on exit
})

async function login(browser: Browser, user: string): Promise<Page> {
  const page = await (await browser.newContext()).newPage()
  page.on('dialog', (d) => d.accept()) // "End lab" confirm
  await page.goto('/')
  await page.locator('#username').fill(user)
  await page.locator('#password').fill(user)
  await page.locator('#kc-login').click()
  await expect(page.getByRole('heading', { name: 'Hearth' })).toBeVisible()
  return page
}

async function typeIn(page: Page, tab: string, command: string) {
  await page.getByRole('tab', { name: tab, exact: true }).click()
  await page.locator(`[data-terminal="${tab}"]`).click()
  await page.keyboard.type(command)
  await page.keyboard.press('Enter')
}

test('a scorer grades a free-text answer and a lab submission; the trainee sees the feedback', async ({ browser }) => {
  // The leader enrolls the trainee in Forge 301; seniors become its scorers by default.
  const leader = await login(browser, 'leader')
  await enroll(leader, 'forge-301', 'trainee@crucible.local')

  // The trainee answers the choice question, writes an answer and attaches a screenshot. Module 2 stays locked.
  const trainee = await login(browser, 'trainee')
  await trainee.getByRole('link', { name: /Forge 301/ }).click()
  await expect(trainee.getByTestId('module-02-review-lab')).toHaveAttribute('data-locked', 'true')
  await trainee.getByTestId('module-01-temper').getByRole('link', { name: 'Quiz' }).click()
  await trainee.getByLabel('Hardens it').check()
  await trainee.getByRole('button', { name: 'Submit answers' }).click()
  await expect(trainee.getByRole('status').filter({ hasText: 'Choices scored.' })).toBeVisible()
  const why = trainee.getByTestId('human-q-why')
  await why.getByLabel('Answer to q-why').fill('Quenched steel is hard but brittle. Tempering trades a little hardness for toughness.')
  await why.getByRole('button', { name: 'Submit for scoring' }).click()
  await expect(why.getByTestId('feedback')).toContainText('Waiting for a scorer')
  const log = trainee.getByTestId('human-q-log')
  await log.getByLabel('Files for q-log').setInputFiles({ name: 'forge.png', mimeType: 'image/png', buffer: Buffer.from('not really a png') })
  await log.getByRole('button', { name: 'Submit for scoring' }).click()
  await expect(log.getByTestId('feedback')).toContainText('Waiting for a scorer')
  await trainee.getByRole('link', { name: 'Back to the training' }).first().click()
  await expect(trainee.getByTestId('module-01-temper')).toContainText('Awaiting the hammer')
  await expect(trainee.getByTestId('module-02-review-lab')).toHaveAttribute('data-locked', 'true')

  // The senior scores the free-text answer, returns the screenshot for rework and signs off the live demo.
  const senior = await login(browser, 'senior')
  await senior.getByRole('link', { name: 'Anvil' }).click()
  await senior.getByRole('link', { name: /Submission from trainee@crucible.local: In two sentences/ }).click()
  await expect(senior.getByTestId('rubric')).toContainText('hard but brittle')
  await expect(senior.getByTestId('answer')).toContainText('Tempering trades')
  await senior.getByLabel('Points', { exact: true }).fill('4')
  await senior.getByLabel('Feedback', { exact: true }).fill('Good. Name toughness as the goal, not just "less brittle".')
  await senior.getByRole('button', { name: 'Score' }).click()
  await expect(senior.getByRole('heading', { name: 'Anvil' })).toBeVisible()
  await senior.getByRole('link', { name: /Submission from trainee@crucible.local: Attach the log/ }).click()
  await expect(senior.getByRole('link', { name: 'forge.png' })).toBeVisible()
  await senior.getByLabel('Feedback', { exact: true }).fill('Attach the log itself, not a screenshot.')
  await senior.getByRole('button', { name: 'Return for rework' }).click()
  await expect(senior.getByRole('heading', { name: 'Anvil' })).toBeVisible()
  await senior.getByRole('listitem', { name: /Sign-off for trainee@crucible.local: Show a scorer/ }).getByRole('button', { name: 'Mark passed' }).click()
  await expect(senior.getByText('No live sign-offs waiting.')).toBeVisible()

  // The trainee sees the score and feedback, and resubmits the log.
  await trainee.goto('/p/forge/forge-301/m/01-temper/quiz')
  await expect(trainee.getByTestId('human-q-why').getByTestId('feedback')).toContainText('Scored 4 / 5')
  await expect(trainee.getByTestId('human-q-why').getByTestId('feedback')).toContainText('Name toughness as the goal')
  await expect(trainee.getByTestId('human-q-log').getByTestId('feedback')).toContainText('Attach the log itself')
  await trainee.getByLabel('Files for q-log').setInputFiles({ name: 'forge.log', mimeType: 'text/plain', buffer: Buffer.from('heat 1200C\nquench\ntemper 200C\n') })
  await trainee.getByTestId('human-q-log').getByRole('button', { name: 'Resubmit for scoring' }).click()
  await expect(trainee.getByTestId('human-q-log').getByTestId('feedback')).toContainText('Waiting for a scorer')

  await senior.reload()
  await senior.getByRole('link', { name: /Submission from trainee@crucible.local: Attach the log/ }).click()
  await expect(senior.getByText('Attach the log itself, not a screenshot.')).toBeVisible() // earlier attempt
  await senior.getByLabel('Points', { exact: true }).fill('2')
  await senior.getByRole('button', { name: 'Score' }).click()
  await expect(senior.getByRole('heading', { name: 'Anvil' })).toBeVisible()

  // All human items scored: module 2 unlocks. The trainee runs the lab on their laptop and submits the review task.
  await trainee.goto('/p/forge/forge-301')
  await expect(trainee.getByTestId('module-02-review-lab')).toHaveAttribute('data-locked', 'false')
  await trainee.getByRole('link', { name: 'Connect your laptop' }).click()
  await trainee.getByRole('button', { name: 'Generate pairing token' }).click()
  const token = (await trainee.getByTestId('pairing-token').textContent())!.trim()
  agent = spawn(process.env.CRUCIBLE_AGENT!, ['--server', 'http://localhost:8080'], { stdio: 'inherit', env: { ...process.env, CRUCIBLE_TOKEN: token } })
  await expect(trainee.getByRole('status').filter({ hasText: /Agent connected/ })).toBeVisible({ timeout: 30_000 })
  await trainee.goto('/p/forge/forge-301/m/02-review-lab/lab')
  await trainee.getByRole('button', { name: 'Ignite the forge' }).click()
  await expect(trainee.getByRole('tab', { name: 'shell', exact: true })).toBeVisible({ timeout: 5 * 60_000 })
  await expect(trainee.getByText('Terminal output is recorded')).toBeVisible()
  await typeIn(trainee, 'shell', 'touch /tmp/lit')
  await trainee.getByRole('button', { name: 'Check' }).click()
  await expect(trainee.getByText('The forge is lit.')).toBeVisible()
  await expect(trainee.getByRole('heading', { name: 'Prove your work' })).toBeVisible({ timeout: 10_000 })
  await typeIn(trainee, 'shell', 'echo tempered > /tmp/proof && cat /tmp/proof')
  await trainee.getByLabel('Notes for the scorer').fill('Wrote the proof file and printed it back.')
  await trainee.getByRole('button', { name: 'Submit for review' }).click()
  await expect(trainee.getByTestId('feedback')).toContainText('Waiting for a scorer')
  await trainee.getByRole('button', { name: 'End lab' }).click() // closes the terminal: its transcript is saved
  await expect(trainee.getByRole('heading', { name: 'The forge has cooled' })).toBeVisible({ timeout: 60_000 })

  // The senior reviews the lab: check results, the transcript, an audited override, then the score.
  await senior.goto('/anvil')
  await senior.getByRole('link', { name: /Submission from trainee@crucible.local: Prove your work/ }).click()
  await expect(senior.getByTestId('rubric')).toContainText('tempered')
  await expect(senior.getByTestId('answer')).toContainText('Wrote the proof file')
  const light = senior.getByRole('region', { name: 'Task Light the forge' })
  await expect(light).toContainText('The forge is lit.')
  await light.getByLabel('Override points').fill('1')
  await light.getByLabel('Reason').fill('Self-reported on a laptop; the transcript shows the file but no check of it.')
  await light.getByRole('button', { name: 'Override' }).click()
  await expect(light.getByTestId('awarded')).toHaveText('1 / 2')
  await senior.getByRole('button', { name: /Show transcript: shell/ }).first().click()
  await expect(senior.getByTestId(/^transcript-/).first()).toContainText('tempered', { timeout: 10_000 })
  await senior.getByLabel('Points', { exact: true }).fill('3')
  await senior.getByLabel('Feedback', { exact: true }).fill('Clean proof. Next time show the file permissions too.')
  await senior.getByRole('button', { name: 'Score' }).click()
  await expect(senior.getByRole('heading', { name: 'Anvil' })).toBeVisible()

  // The trainee sees the feedback on the cooled lab, and the module is forged.
  await trainee.reload()
  await expect(trainee.getByTestId('feedback')).toContainText('Scored 3 / 3')
  await expect(trainee.getByTestId('feedback')).toContainText('Clean proof.')
  await trainee.getByRole('link', { name: 'Back to the training' }).first().click()
  await expect(trainee.getByTestId('module-02-review-lab')).toHaveClass(/complete/)
})
