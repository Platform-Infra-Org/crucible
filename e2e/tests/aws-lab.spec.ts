import { expect, test, type Browser, type Page } from '@playwright/test'

async function login(browser: Browser, user: string): Promise<Page> {
  const page = await (await browser.newContext()).newPage()
  page.on('dialog', (d) => d.accept())
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

test('an aws lab is estimated, approved, provisioned, checked, destroyed and swept; spend shows on the Ledger', async ({ browser }) => {
  // The leader enrols the team and the trainee in Forge 401 (two bot commits), as approvals.spec.ts does for 201.
  const leader = await login(browser, 'leader')
  await leader.getByRole('link', { name: 'Team', exact: true }).click()
  await leader.getByLabel('Training to enroll').selectOption('forge-401')
  await leader.getByRole('button', { name: 'Enroll the team' }).click()
  await expect(leader.getByRole('heading', { name: /Program settings/ })).toBeVisible()
  await leader.getByRole('checkbox', { name: 'trainee@crucible.local' }).check()
  await leader.getByRole('button', { name: 'Save program' }).click()
  await expect(leader.getByRole('status').filter({ hasText: /Saved to git/ })).toBeVisible()

  // Estimated ($0.04/h × 1 h in dry-run pricing). AWS labs always need an approver.
  const trainee = await login(browser, 'trainee')
  await expect(trainee.getByRole('link', { name: /Forge 401/ })).toBeVisible({ timeout: 60_000 })
  await trainee.goto('/p/forge/forge-401/m/01-cloud-heat/lab')
  await trainee.getByRole('button', { name: 'Request approval ($0.04)' }).click()
  await expect(trainee.getByTestId('request-status')).toContainText('Waiting for approval')

  // Approved.
  await leader.getByRole('link', { name: 'Approvals' }).click()
  const card = leader.getByRole('listitem', { name: 'Request from trainee@crucible.local' })
  await expect(card.getByTestId('estimate')).toContainText('$0.04')
  await card.getByRole('button', { name: 'Approve' }).click()

  // Provisioned: the namespace, the credentials secret, the dind workspace (pulls amazon/aws-cli) and tf-apply.
  await expect(trainee.getByRole('tab', { name: 'workspace', exact: true })).toBeVisible({ timeout: 10 * 60_000 })

  // Checked server-side, in the workspace, with the mounted lab credentials.
  await typeIn(trainee, 'workspace', "aws configure list | awk '/region/ {print $2}' > ~/region.txt")
  // The aws CLI takes a few seconds to start in dind, so Check until the file is there.
  await expect(async () => {
    await trainee.getByRole('button', { name: 'Check' }).click()
    await expect(trainee.getByText("Your CLI points at eu-west-1 with your lab's credentials.")).toBeVisible({ timeout: 5_000 })
  }).toPass({ timeout: 60_000 })

  // Destroyed: End returns at once; tf-destroy and the tag sweep run in the background.
  await trainee.getByRole('button', { name: 'End lab' }).click()
  await expect(trainee.getByRole('heading', { name: 'The forge has cooled' })).toBeVisible({ timeout: 5 * 60_000 })

  // Swept, and on the Ledger: the hand-made volume the dry run left behind, and the lab's (fake) AWS bill.
  const admin = await login(browser, 'admin')
  await admin.getByRole('link', { name: 'Ledger' }).click()
  await admin.getByRole('button', { name: 'Refresh now' }).click()
  const findings = admin.getByTestId('reaper-findings')
  await expect(findings).toContainText(/volume\/vol-[0-9a-f]{12}/)
  await expect(findings).toContainText('end-of-lab sweep')
  await expect(findings).toContainText('deleted')
  const row = admin.getByTestId('ledger-labs').getByRole('row').filter({ hasText: 'forge-401 / 01-cloud-heat' })
  await expect(row).toContainText('$0.11 (so far)')
})
