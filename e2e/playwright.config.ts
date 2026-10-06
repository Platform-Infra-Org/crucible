import { defineConfig } from '@playwright/test'

export default defineConfig({
  testDir: './tests',
  timeout: 8 * 60_000,
  expect: { timeout: 20_000 },
  workers: 1,
  projects: [
    { name: 'local', testIgnore: /cluster-lab\.spec\.ts/ },
    // Needs make cluster-check (kind). Runs after `local`, which completes modules 01–02 of the linear training.
    ...(process.env.CLUSTER === '1' ? [{ name: 'cluster', testMatch: /cluster-lab\.spec\.ts/, dependencies: ['local'] }] : []),
  ],
  use: { baseURL: 'http://localhost:8080', trace: 'retain-on-failure', viewport: { width: 1440, height: 900 } },
})
