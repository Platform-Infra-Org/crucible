import { expect, type Browser, type Page } from '@playwright/test'

export async function login(browser: Browser, user: string): Promise<Page> {
  const page = await (await browser.newContext()).newPage()
  page.on('dialog', (d) => d.accept())
  await page.goto('/')
  await page.locator('#username').fill(user)
  await page.locator('#password').fill(user)
  await page.locator('#kc-login').click()
  await expect(page.getByRole('heading', { name: 'Hearth' })).toBeVisible()
  return page
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
