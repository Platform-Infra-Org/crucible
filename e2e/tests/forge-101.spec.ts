import { expect, test, type Page } from '@playwright/test'
import { spawn, type ChildProcess } from 'node:child_process'

let agent: ChildProcess | undefined
test.afterAll(() => {
  agent?.kill('SIGTERM') // the agent tears down its compose projects on exit
})

async function typeIn(page: Page, tab: string, command: string) {
  await page.getByRole('tab', { name: tab, exact: true }).click()
  const term = page.locator(`[data-terminal="${tab}"]`)
  await term.click()
  await page.keyboard.type(command)
  await page.keyboard.press('Enter')
}

async function sortOrder(page: Page, testId: string, target: string[]) {
  for (let i = 0; i < target.length; i++) {
    const current = (await page.getByTestId(testId).locator('li > span').allTextContents()).map((s) => s.trim())
    for (let j = current.indexOf(target[i]); j > i; j--) {
      await page.getByRole('button', { name: `Move ${target[i]} up` }).click()
    }
  }
}

test('a trainee completes Forge 101 using only a local lab', async ({ page }) => {
  // Paid hints and End lab ask window.confirm; a trainee says yes.
  page.on('dialog', (d) => d.accept())

  // SSO login through Keycloak
  await page.goto('/')
  await page.locator('#username').fill('trainee')
  await page.locator('#password').fill('trainee')
  await page.locator('#kc-login').click()
  await expect(page.getByRole('heading', { name: 'Hearth' })).toBeVisible()

  // Pair the laptop agent
  await page.getByRole('link', { name: 'Connect your laptop' }).click()
  await page.getByRole('button', { name: 'Generate pairing token' }).click()
  const token = (await page.getByTestId('pairing-token').textContent())!.trim()
  agent = spawn(process.env.CRUCIBLE_AGENT!, ['--server', 'http://localhost:8080'], { stdio: 'inherit', env: { ...process.env, CRUCIBLE_TOKEN: token } })
  // Case-sensitive: getByText('Agent connected') would also match "No agent connected yet."
  await expect(page.getByRole('status').filter({ hasText: /Agent connected/ })).toBeVisible({ timeout: 30_000 })

  // Module 1: read + quiz; module 2 starts locked
  await page.getByRole('link', { name: 'Hearth', exact: true }).click()
  await page.getByRole('link', { name: /Forge 101/ }).click()
  await expect(page.getByTestId('module-02-first-lab')).toHaveAttribute('data-locked', 'true')
  await page.getByRole('link', { name: 'How We Work' }).click()
  await page.getByRole('button', { name: 'Mark as read' }).click()
  await page.getByTestId('module-01-welcome').getByRole('link', { name: 'Quiz' }).click()

  await page.getByLabel('docker ps').check()
  for (const r of ['Docker Hub', 'GitHub Container Registry', 'Amazon ECR']) await page.getByLabel(r).check()
  await page.getByTestId('answer-q-port').fill('80')
  await page.getByTestId('answer-q-version').fill('1.2.3')
  await sortOrder(page, 'order-q-order', ['Heat', 'Hammer', 'Quench', 'Temper'])
  await page.getByLabel('git', { exact: true }).selectOption({ label: 'version control' })
  await page.getByLabel('docker', { exact: true }).selectOption({ label: 'containers' })
  await page.getByLabel('terraform', { exact: true }).selectOption({ label: 'infrastructure as code' })
  await page.getByRole('button', { name: 'Submit answers' }).click()
  await expect(page.getByRole('status').filter({ hasText: 'Passed' })).toBeVisible()

  // Module 2 unlocked: read, then the local lab
  await page.getByRole('link', { name: 'Back to the training' }).first().click()
  await expect(page.getByTestId('module-02-first-lab')).toHaveAttribute('data-locked', 'false')
  await page.getByRole('link', { name: 'Before the Lab' }).click()
  await page.getByRole('button', { name: 'Mark as read' }).click()
  await page.getByTestId('module-02-first-lab').getByRole('link', { name: 'Lab', exact: true }).click()
  await page.getByRole('button', { name: 'Ignite the forge' }).click()
  await expect(page.getByRole('tab', { name: 'shell', exact: true })).toBeVisible({ timeout: 5 * 60_000 }) // first run pulls images
  await expect(page.getByTestId('lab-timer')).toHaveText(/\d{2}:\d{2}/)
  const full = page.getByRole('button', { name: 'Full screen' })
  await full.click()
  await expect(full).toHaveAttribute('aria-pressed', 'true')
  await full.click()
  await expect(full).toHaveAttribute('aria-pressed', 'false')

  // Task 1: create the file in the shell terminal, then Check
  await typeIn(page, 'shell', "echo 'hello forge' > /tmp/forged.txt")
  await page.getByRole('button', { name: 'Check' }).click()
  await expect(page.getByText('Your first ingot is cast.')).toBeVisible()

  // Task 2: setup breaks nginx; answer the terminal quiz
  await expect(page.getByLabel('Your answer')).toBeVisible({ timeout: 90_000 })
  await page.getByLabel('Your answer').fill('8081')
  await page.getByRole('button', { name: 'Check' }).click()
  await expect(page.getByText('Correct: the forge moved to 8081.')).toBeVisible()

  // Task 3: reveal the first hint (only the current open task shows hints), fix nginx in the web terminal, Check
  await expect(page.getByText('Put it back')).toBeVisible({ timeout: 30_000 })
  await page.getByRole('button', { name: /^Hint/ }).click()
  await expect(page.getByText('Hint 1')).toBeVisible()
  await typeIn(page, 'web', "sed -i 's/8081;/80;/g' /etc/nginx/conf.d/default.conf && nginx -s reload")
  await page.getByRole('button', { name: 'Check' }).click()
  await expect(page.getByText('The forge burns bright on port 80 again.')).toBeVisible()
  await expect(page.getByText(/Lab forged/)).toBeVisible()

  // End the lab (confirm accepted above): summary shows, progress kept
  await page.getByRole('button', { name: 'End lab' }).click()
  await expect(page.getByRole('heading', { name: 'The forge has cooled' })).toBeVisible({ timeout: 60_000 })
  await page.getByRole('link', { name: 'Back to the training' }).click()
  await expect(page.getByTestId('module-02-first-lab')).toHaveClass(/complete/)
})
