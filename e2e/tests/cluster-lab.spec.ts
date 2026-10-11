import { expect, test, type Page } from '@playwright/test'

async function typeIn(page: Page, tab: string, command: string) {
  await page.getByRole('tab', { name: tab, exact: true }).click()
  await page.locator(`[data-terminal="${tab}"]`).click()
  await page.keyboard.type(command)
  await page.keyboard.press('Enter')
}

const labAPI = '/api/programs/forge/forge-101/modules/03-cluster-heat/lab'

test('Forge 101 cluster lab: terminals over exec, checks run by the server', async ({ page }) => {
  page.on('dialog', (d) => d.accept())
  await page.goto('/')
  await page.getByRole('button', { name: 'Enter the forge' }).click() // the gate, then the identity provider
  await page.locator('#username').fill('trainee')
  await page.locator('#password').fill('trainee')
  await page.locator('#kc-login').click()
  await expect(page.getByRole('heading', { name: 'Hearth' })).toBeVisible()

  // Module 03 is unlocked because the `local` project finished module 02. No laptop agent is running.
  await page.getByRole('link', { name: /Forge 101/ }).click()
  await expect(page.getByTestId('module-03-cluster-heat')).toHaveAttribute('data-locked', 'false')
  await page.getByTestId('module-03-cluster-heat').getByRole('link', { name: 'Lab', exact: true }).click()
  await page.getByRole('button', { name: 'Ignite the forge' }).click()
  // First run: kind pulls docker:dind, then dockerd pulls alpine + nginx inside the lab pod.
  await expect(page.getByRole('tab', { name: 'shell', exact: true })).toBeVisible({ timeout: 8 * 60_000 })

  // Task 1: the terminal is a Kubernetes exec stream; the check runs server-side.
  await typeIn(page, 'shell', "echo 'hello crucible' > /tmp/cast.txt")
  await page.getByRole('button', { name: 'Check' }).click()
  await expect(page.getByText('Cast in the crucible.')).toBeVisible()

  // Task 2: the setup (exec, out of band) breaks nginx; fix it in the web terminal.
  await expect(page.getByText('Someone moved nginx')).toBeVisible({ timeout: 90_000 })
  await typeIn(page, 'web', "sed -i 's/8081;/80;/g' /etc/nginx/conf.d/default.conf && nginx -s reload")
  await page.getByRole('button', { name: 'Check' }).click()
  await expect(page.getByText('The crucible burns bright on port 80.')).toBeVisible()
  await expect(page.getByText(/Lab forged/)).toBeVisible()

  // Done-when: checks are not self-reported.
  const info = await (await page.request.get(labAPI)).json()
  expect(info.runtime).toBe('cluster')
  expect(info.lab.self_reported).toBe(false)

  await page.getByRole('button', { name: 'End lab' }).click()
  await expect(page.getByRole('heading', { name: 'The forge has cooled' })).toBeVisible({ timeout: 60_000 })
})
