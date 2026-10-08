import { expect, test } from '@playwright/test'
import { login } from './helpers'

// Configuration lives in Postgres: an admin registers a training, starts it for a team, enrolls a team member and a
// brand-new person, and hands out a role, all in the app with no YAML anywhere. Idempotent on a KEEP=1 stack: the
// newcomer's address carries a run id, and every step tolerates what an earlier run left behind.
const run = Date.now().toString(36)

test('an admin registers a training, starts it for a team, enrolls people and gives out a role; the trainee sees it', async ({ browser }) => {
  const admin = await login(browser, 'admin')
  await admin.getByRole('button', { name: 'Administrator' }).click()
  await admin.getByRole('menuitem', { name: 'Trainings' }).click()
  await expect(admin.getByRole('heading', { name: 'Manage trainings' })).toBeVisible()
  const list = admin.getByRole('navigation', { name: 'Trainings' })
  if (!(await list.getByRole('link', { name: /forge-001/ }).count())) {
    await admin.getByRole('link', { name: '+ New training' }).click()
    await admin.getByLabel('Training id').fill('forge-001')
    await admin.getByLabel('Repository URL').fill('file:///git/forge-001.git') // seeded by scripts/seed-git.sh
    await admin.getByRole('button', { name: 'Register training' }).click()
  } else {
    await list.getByRole('link', { name: /forge-001/ }).click()
  }
  await expect(admin.getByRole('heading', { name: 'Forge 001: Striking the Spark' })).toBeVisible()
  await expect(admin.getByText('file:///git/forge-001.git')).toBeVisible() // admins see the source

  const panel = admin.locator('#team-forge')
  if (!(await panel.count())) {
    await admin.getByLabel('Team to start it for').selectOption('forge')
    await admin.getByRole('button', { name: 'Start for this team' }).click()
  }
  await expect(panel).toBeVisible()
  // Nobody named for a role yet: the defaults show who holds it today.
  await expect(panel.getByText('Default: leader@crucible.local').first()).toBeVisible()

  // A team member is enrolled at once.
  const trainee = panel.getByRole('button', { name: 'Remove trainee@crucible.local from enrolled' })
  if (!(await trainee.count())) {
    await panel.getByLabel('Enroll someone in The Forge').fill('trainee@crucible.local')
    await panel.getByRole('button', { name: 'Enroll', exact: true }).click()
  }
  await expect(trainee).toBeVisible()

  // Someone new joins the team as a trainee and is enrolled in one step (login() accepts the confirm dialog).
  const newcomer = `newcomer-${run}@crucible.local`
  await panel.getByLabel('Enroll someone in The Forge').fill(newcomer)
  await panel.getByRole('button', { name: 'Enroll', exact: true }).click()
  await expect(panel.getByRole('button', { name: `Remove ${newcomer} from enrolled` })).toBeVisible()

  // A role, given and taken back: the default returns.
  await panel.getByLabel('Add an approver').selectOption('senior@crucible.local')
  const approver = panel.getByRole('button', { name: 'Remove senior@crucible.local from approvers' })
  await expect(approver).toBeVisible()
  await approver.click()
  await expect(approver).toHaveCount(0)

  // The newcomer is on the team page now, and the program links back here.
  await admin.goto('/teams/forge')
  await expect(admin.getByLabel('Trainees')).toHaveValue(new RegExp(newcomer))
  await expect(admin.getByRole('link', { name: 'Manage' }).first()).toBeVisible()

  // The trainee finds the new training on the Hearth and reads it.
  const t = await login(browser, 'trainee')
  await t.getByRole('link', { name: /Forge 001/ }).click()
  await t.getByTestId('module-01-spark').getByRole('link').first().click()
  await expect(t.getByText('Every blade starts as a spark.')).toBeVisible()
})
