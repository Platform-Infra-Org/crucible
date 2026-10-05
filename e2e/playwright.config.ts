import { defineConfig } from '@playwright/test'

export default defineConfig({
  testDir: './tests',
  timeout: 8 * 60_000,
  expect: { timeout: 20_000 },
  workers: 1,
  use: { baseURL: 'http://localhost:8080', trace: 'retain-on-failure', viewport: { width: 1440, height: 900 } },
})
