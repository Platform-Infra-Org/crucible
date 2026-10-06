import { expect, test, type Browser, type Page } from '@playwright/test'
import { spawn, type ChildProcess } from 'node:child_process'

let agent: ChildProcess | undefined
test.afterAll(() => {
  agent?.kill('SIGTERM') // the agent tears down its compose projects on exit
})

// Each person gets their own browser context (own session cookie), signed in through Keycloak.
async function login(browser: Browser, user: string): Promise<Page> {
  const page = await (await browser.newContext()).newPage()
  page.on('dialog', (d) => d.accept()) // kill switch and "End lab" confirm
  await page.goto('/')
  await page.locator('#username').fill(user)
  await page.locator('#password').fill(user)
  await page.locator('#kc-login').click()
  await expect(page.getByRole('heading', { name: 'Hearth' })).toBeVisible()
  return page
}

test('a leader enrolls a trainee via git, approves a paid lab, and an admin pauses all labs', async ({ browser }) => {
  // The leader enrolls the team in Forge 201 and the trainee in it: two bot commits to the platform repo.
  const leader = await login(browser, 'leader')
  await leader.getByRole('link', { name: 'Team', exact: true }).click()
  await expect(leader.getByRole('heading', { name: 'The Forge' })).toBeVisible()
  await leader.getByLabel('Training to enroll').selectOption('forge-201')
  await leader.getByRole('button', { name: 'Enroll the team' }).click()
  await expect(leader.getByRole('heading', { name: /Program settings/ })).toBeVisible()
  await leader.getByRole('checkbox', { name: 'trainee@crucible.local' }).check()
  await leader.getByRole('button', { name: 'Save program' }).click()
  await expect(leader.getByRole('status').filter({ hasText: /Saved to git/ })).toBeVisible()

  // The trainee now sees Forge 201 (sync picked up the commit), pairs the laptop and asks for the paid lab.
  const trainee = await login(browser, 'trainee')
  await expect(trainee.getByRole('link', { name: /Forge 201/ })).toBeVisible()
  await trainee.getByRole('link', { name: 'Connect your laptop' }).click()
  await trainee.getByRole('button', { name: 'Generate pairing token' }).click()
  const token = (await trainee.getByTestId('pairing-token').textContent())!.trim()
  agent = spawn(process.env.CRUCIBLE_AGENT!, ['--server', 'http://localhost:8080'], { stdio: 'inherit', env: { ...process.env, CRUCIBLE_TOKEN: token } })
  await expect(trainee.getByRole('status').filter({ hasText: /Agent connected/ })).toBeVisible({ timeout: 30_000 })
  await trainee.goto('/p/forge/forge-201/m/01-paid-lab/lab')
  await trainee.getByRole('button', { name: 'Request approval ($0.50)' }).click()
  await expect(trainee.getByTestId('request-status')).toContainText('Waiting for approval')

  // The leader (default approver, $0.50 is within tier 1) approves from the inbox.
  await leader.getByRole('link', { name: 'Approvals' }).click()
  const card = leader.getByRole('listitem', { name: 'Request from trainee@crucible.local' })
  await expect(card.getByTestId('estimate')).toContainText('$0.50')
  await card.getByRole('button', { name: 'Approve' }).click()
  await expect(leader.getByText('Nothing waiting. The forge is quiet.')).toBeVisible()

  // The approved lab starts on the trainee's laptop (the lobby polls the pending request).
  await expect(trainee.getByRole('tab', { name: 'shell', exact: true })).toBeVisible({ timeout: 5 * 60_000 })

  // An admin pulls the kill switch: the lab dies and new requests are blocked.
  const admin = await login(browser, 'admin')
  await admin.getByRole('link', { name: 'Forge Status' }).click()
  await admin.getByRole('button', { name: 'Pause all labs' }).click()
  await expect(admin.getByTestId('kill-switch-status')).toContainText('Labs are paused')
  await expect(trainee.getByText('An admin paused all labs.')).toBeVisible({ timeout: 60_000 })
  await trainee.reload()
  await expect(trainee.getByText('Labs are paused by an admin.')).toBeVisible()

  // Resume so the rest of the suite can run labs.
  await admin.getByRole('button', { name: 'Resume labs' }).click()
  await expect(admin.getByTestId('kill-switch-status')).toContainText('Labs are running')

  // The Ledger shows the leader the team's budget burn and the paid lab (local-check has no AWS: estimates only).
  await leader.getByRole('link', { name: 'Ledger' }).click()
  await expect(leader.getByRole('meter', { name: 'The Forge spend' })).toBeVisible()
  await expect(leader.getByTestId('ledger-labs')).toContainText('forge-201 / 01-paid-lab')
})
