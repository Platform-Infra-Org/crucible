import { expect, test } from '@playwright/test'
import { login, watchCsp } from './helpers'

test('each role opens Docs on its own section; ? links open the page about the screen; no CSP violations', async ({ browser }) => {
  for (const [user, section] of [['trainee', /\/docs\/trainees\//], ['leader', /\/docs\/leaders\//], ['admin', /\/docs\/admins\//], ['senior', /\/docs\/(scorers|authors)\//]] as const) {
    const page = await login(browser, user)
    const csp = await watchCsp(page)
    await page.getByRole('link', { name: 'Docs', exact: true }).click()
    await expect(page).toHaveURL(section)
    await expect(page.getByRole('navigation', { name: 'Docs' })).toBeVisible()
    await expect(page.getByText(/Last updated with Crucible/)).toBeVisible()
    expect(csp, `${user}: ${csp.join('\n')}`).toEqual([])
  }
  const trainee = await login(browser, 'trainee')
  const csp = await watchCsp(trainee)
  for (const [path, doc] of [['/', 'getting-started/the-hearth'], ['/trainings', 'getting-started/the-hearth'], ['/labs', 'trainees/cluster-and-aws-labs'],
    ['/connect', 'trainees/connect-your-laptop'], ['/settings', 'getting-started/themes-and-calm']] as const) {
    await trainee.goto(path)
    await trainee.getByRole('link', { name: 'Help for this page' }).click()
    await expect(trainee).toHaveURL(new RegExp(`/docs/${doc}$`))
    await expect(trainee.locator('.prose h1')).toBeVisible()
  }
  expect(csp, csp.join('\n')).toEqual([])
})
