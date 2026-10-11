import { spawn, type ChildProcess } from 'node:child_process'
import { expect, test, type Page } from '@playwright/test'

async function typeIn(page: Page, tab: string, command: string) {
  await page.getByRole('tab', { name: tab, exact: true }).click()
  await page.locator(`[data-terminal="${tab}"]`).click()
  await page.keyboard.type(command)
  await page.keyboard.press('Enter')
}

// Check again until it passes, as a trainee does while the rollout they started in the terminal finishes.
async function checkUntil(page: Page, passed: string | RegExp) {
  await expect(async () => {
    await page.getByRole('button', { name: 'Check' }).click()
    await expect(page.getByText(passed)).toBeVisible({ timeout: 5_000 })
  }).toPass({ timeout: 90_000 })
}

const labAPI = '/api/programs/forge/forge-101/modules/03-cluster-heat/lab'
let agent: ChildProcess | undefined
test.afterAll(() => { agent?.kill('SIGTERM') })

test('Forge 101 cluster lab: Helm on your own kind cluster through the agent', async ({ page }) => {
  page.on('dialog', (d) => d.accept())
  await page.goto('/')
  await page.getByRole('button', { name: 'Enter the forge' }).click()
  await page.locator('#username').fill('trainee')
  await page.locator('#password').fill('trainee')
  await page.locator('#kc-login').click()
  await expect(page.getByRole('heading', { name: 'Hearth' })).toBeVisible()

  // Pair the agent with the kind cluster's own kubeconfig (never ~/.kube/config).
  const res = await page.request.post('/api/agent/tokens', { data: {}, headers: { Origin: 'http://localhost:8080' } })
  expect(res.ok()).toBe(true)
  const { token } = await res.json()
  agent = spawn(process.env.CRUCIBLE_AGENT!, ['--server', 'http://localhost:8080', '--kubeconfig', process.env.CRUCIBLE_E2E_KUBECONFIG!],
    { stdio: 'inherit', env: { ...process.env, CRUCIBLE_TOKEN: token } })

  await page.getByRole('link', { name: /Forge 101/ }).click()
  await page.getByTestId('module-03-cluster-heat').getByRole('link', { name: 'Lab', exact: true }).click()
  await expect(page.getByRole('option', { name: /kind-crucible-m4/ })).toBeAttached({ timeout: 30_000 })
  await page.getByLabel('Run it on').selectOption('kind-crucible-m4')
  await page.getByRole('button', { name: /^Ignite (the forge|again)$/ }).click() // again: a KEEP=1 stack's earlier lab
  await expect(page.getByRole('tab', { name: 'shell', exact: true })).toBeVisible({ timeout: 5 * 60_000 })
  await expect(page.getByText('on kind-crucible-m4')).toBeVisible()

  // Task 1: kubectl in the toolbox, a script check.
  await typeIn(page, 'shell', 'kubectl scale deploy/forge --replicas=2 && kubectl rollout status deploy/forge')
  await checkUntil(page, 'Two fires burn in the forge.') // the second replica takes a few seconds to be ready

  // Task 2: the setup breaks the image; a ready: check passes once it is fixed.
  await expect(page.getByText('Relight the forge')).toBeVisible({ timeout: 90_000 })
  await page.getByRole('button', { name: 'Check' }).click()
  await expect(page.locator('.check-output.bad')).toContainText(/deployment\/forge: \d of 2 ready/) // the failed check names what is not ready
  await typeIn(page, 'shell', 'kubectl set image deploy/forge web=nginx:1.27-alpine && kubectl rollout status deploy/forge')
  await checkUntil(page, /Lab forged/)

  const info = await (await page.request.get(labAPI)).json()
  expect(info.runtime).toBe('cluster')
  expect(info.lab.self_reported).toBe(true)
  expect(info.lab.cluster_context).toBe('kind-crucible-m4')
  expect(info.estimate_usd).toBe(0)

  const ns = `lab-${info.lab.id}`
  await page.getByRole('button', { name: 'End lab' }).click()
  await expect(page.getByRole('heading', { name: 'The forge has cooled' })).toBeVisible({ timeout: 3 * 60_000 })
  // The namespace is gone from the cluster.
  const { execFileSync } = await import('node:child_process')
  const left = execFileSync('kubectl', ['--kubeconfig', process.env.CRUCIBLE_E2E_KUBECONFIG!, 'get', 'ns', ns, '--ignore-not-found', '-o', 'name']).toString().trim()
  expect(left).toBe('')
})
