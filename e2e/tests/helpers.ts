import { expect, type Browser, type Page } from '@playwright/test'

export async function login(browser: Browser, user: string): Promise<Page> {
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

// contentEdits opens a training on Manage trainings, the way a person gets there, and returns its Content edits panel:
// where an edit starts and where drafts and edits waiting for review are listed.
export async function contentEdits(page: Page, training: string) {
  await page.getByRole('link', { name: 'Trainings', exact: true }).click()
  await page.getByRole('link', { name: 'Manage trainings' }).click()
  await page.getByRole('navigation', { name: 'Trainings' }).getByRole('link', { name: new RegExp(`\\b${training}\\b`) }).click()
  await expect(page).toHaveURL(new RegExp(`/trainings/manage/${training}$`))
  const panel = page.getByRole('region', { name: 'Content edits' })
  await expect(panel).toBeVisible()
  return panel
}

// addParticipant does what a training's managers do on Manage trainings: give someone (an email or a group) the
// participant role. Idempotent, so it runs again on a KEEP=1 stack.
export async function addParticipant(page: Page, training: string, who: string) {
  await page.getByRole('link', { name: 'Trainings', exact: true }).click()
  await page.getByRole('link', { name: 'Manage trainings' }).click()
  await page.getByRole('navigation', { name: 'Trainings' }).getByRole('link', { name: new RegExp(`\\b${training}\\b`) }).click()
  // The page opens on the first training: wait for the one asked for, or the checks below read the wrong one.
  await expect(page).toHaveURL(new RegExp(`/trainings/manage/${training}$`))
  await expect(page.locator('.manage-detail > p code')).toHaveText(training)
  const panel = page.getByRole('region', { name: /^Roles in / })
  const chip = panel.getByRole('button', { name: `Remove ${who} from participants` })
  if (!(await chip.count())) {
    await panel.getByLabel('Add to participants').fill(who)
    await panel.getByLabel('Add to participants').press('Enter')
  }
  await expect(chip).toBeVisible()
}

// setEditorText replaces the open file's text in Monaco: select all, then one multi-character input (Monaco applies no
// auto-indent or auto-close to it). Callers check the result through the preview or the saved draft.
export async function setEditorText(page: Page, text: string) {
  await page.getByTestId('code-editor').locator('.view-lines').click()
  await page.keyboard.press('ControlOrMeta+A')
  await page.keyboard.insertText(text)
}

// watchCsp collects Content-Security-Policy violations on this page from now on: Chromium's console report and the
// securitypolicyviolation event, on the current document and every later one.
export async function watchCsp(page: Page): Promise<string[]> {
  const seen: string[] = []
  page.on('console', (m) => { if (/Content.Security.Policy/i.test(m.text())) seen.push(m.text()) })
  const listen = () => document.addEventListener('securitypolicyviolation', (e) => console.error(`Content Security Policy violation: ${e.violatedDirective} ${e.blockedURI}`))
  await page.addInitScript(listen)
  await page.evaluate(listen)
  return seen
}
