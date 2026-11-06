import { expect, test, type Browser, type Page } from '@playwright/test'
import { execFileSync } from 'node:child_process'
import { addParticipant } from './helpers'

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

async function typeIn(page: Page, tab: string, command: string) {
  await page.getByRole('tab', { name: tab, exact: true }).click()
  await page.locator(`[data-terminal="${tab}"]`).click()
  await page.keyboard.type(command)
  await page.keyboard.press('Enter')
}

// crucible labs in the server's container, as the nightly CronJob and crucible aws sleep run it.
function crucibleLabs(...args: string[]) {
  execFileSync('docker', ['exec', process.env.CRUCIBLE_E2E_API!, 'crucible', 'labs', ...args], { stdio: 'inherit' })
}

test('an aws lab is estimated, approved, provisioned, checked, destroyed and swept; spend shows on the Ledger', async ({ browser }) => {
  // The leader makes the trainee a participant of Forge 401 (The Forge owns it), as approvals.spec.ts does for 201.
  const leader = await login(browser, 'leader')
  await addParticipant(leader, 'forge-401', 'trainee@crucible.local')

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

  // Swept, and on the Ledger: the hand-made volume the dry run left behind, what the lab was metered at, and its
  // (fake) AWS bill.
  const admin = await login(browser, 'admin')
  await admin.getByRole('link', { name: 'Ledger' }).click()
  await admin.getByRole('button', { name: 'Refresh now' }).click()
  const findings = admin.getByTestId('reaper-findings')
  await expect(findings).toContainText(/volume\/vol-[0-9a-f]{12}/)
  await expect(findings).toContainText('end-of-lab sweep')
  await expect(findings).toContainText('deleted')
  const row = admin.getByTestId('ledger-labs').getByRole('row').filter({ hasText: 'forge-401 / 01-cloud-heat' })
  await expect(row).toContainText('$0.11 (so far)')
  // Metered from the fake apply's Terraform state (a t3.nano and its root volume) at the dry run's fixture prices:
  // under a cent an hour, so a small dollar figure, not '—' (never metered).
  const headers = await admin.getByTestId('ledger-labs').getByRole('columnheader').allTextContents()
  const metered = headers.indexOf('Metered')
  expect(metered).toBeGreaterThanOrEqual(0) // nth(-1) would silently check the last column
  await expect(row.getByRole('cell').nth(metered)).toHaveText(/^\$\d+\.\d{2}$/)
})

test('forge 402: the GRUB rescue lab is priced; Locked out refuses to start without the lab network', async ({ browser }) => {
  const leader = await login(browser, 'leader')
  await addParticipant(leader, 'forge-402', 'trainee@crucible.local')

  // Priced from the dry run's rates ($0.03/h × 2 h). The dry run cannot boot a machine, so this test starts neither lab
  // for real.
  const trainee = await login(browser, 'trainee')
  await expect(trainee.getByRole('link', { name: /Forge 402/ })).toBeVisible({ timeout: 60_000 })
  await trainee.goto('/p/forge/forge-402/m/01-grub-rescue/lab')
  await expect(trainee.getByRole('button', { name: 'Request approval ($0.06)' })).toBeEnabled()

  // Locked out sets aws.direct_ssh, and cluster-check's install has no lab network (no labs-init): approved, it fails
  // before anything is created, and the page says what to run.
  await trainee.goto('/p/forge/forge-402/m/02-locked-out/lab')
  await trainee.getByRole('button', { name: 'Request approval ($0.06)' }).click()
  await expect(trainee.getByTestId('request-status')).toContainText('Waiting for approval')
  await leader.getByRole('link', { name: 'Approvals' }).click()
  const card = leader.getByRole('listitem', { name: 'Request from trainee@crucible.local' }).filter({ hasText: 'forge / forge-402' })
  await card.getByRole('button', { name: 'Approve' }).click()
  const failed = trainee.getByText(/^The lab failed to start: direct_ssh needs this install's lab network in eu-west-1/)
  await expect(failed).toBeVisible({ timeout: 5 * 60_000 })
  await expect(failed).toContainText('crucible aws labs-init --vpc-id --subnet-ids')
})

test('an aws lab sleeps with the platform and wakes, with its files, when its trainee opens it', async ({ browser }) => {
  const leader = await login(browser, 'leader')
  await addParticipant(leader, 'forge-401', 'trainee@crucible.local')
  const trainee = await login(browser, 'trainee')
  await trainee.goto('/p/forge/forge-401/m/01-cloud-heat/lab')
  await trainee.getByRole('button', { name: 'Request approval ($0.04)' }).click()
  await leader.getByRole('link', { name: 'Approvals' }).click()
  await leader.getByRole('listitem', { name: 'Request from trainee@crucible.local' }).getByRole('button', { name: 'Approve' }).click()
  await expect(trainee.getByRole('tab', { name: 'workspace', exact: true })).toBeVisible({ timeout: 10 * 60_000 })
  // Something only this container has, checked after the wake.
  await typeIn(trainee, 'workspace', "aws configure list | awk '/region/ {print $2}' > ~/region.txt")
  await trainee.waitForTimeout(5_000) // the aws CLI starts slowly in dind

  // Bedtime: the dry run's machine stops (it isn't launched for hibernation) and the workspace pod goes.
  crucibleLabs('bedtime', '--wait', '10m')
  await expect(trainee.getByRole('heading', { name: 'Your lab is asleep' })).toBeVisible({ timeout: 60_000 })
  await expect(trainee.getByText('Crucible stopped your machines last night: they restart from their disks.')).toBeVisible()

  // The morning: a real node restarts the server, which ends the bedtime; here it is cancelled.
  crucibleLabs('bedtime', '--cancel')
  await trainee.getByRole('button', { name: 'Wake it' }).click()
  await expect(trainee.getByText("Your machines restarted from their disks; anything you hadn't saved is gone.")).toBeVisible({ timeout: 5 * 60_000 })

  // The same container came back on its volume: the file is still there, and the lab's credentials work.
  // Task 1 passes and the tasks move on to task 2, so wait for the passed mark, not the message.
  await expect(async () => {
    await trainee.getByRole('button', { name: 'Check' }).click()
    await expect(trainee.getByRole('button', { name: 'Task 1: Find your region (passed)' })).toBeVisible({ timeout: 5_000 })
  }).toPass({ timeout: 60_000 })
  await trainee.getByRole('button', { name: 'End lab' }).click()
  await expect(trainee.getByRole('heading', { name: 'The forge has cooled' })).toBeVisible({ timeout: 5 * 60_000 })
})
