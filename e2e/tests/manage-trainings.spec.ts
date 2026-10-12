import { expect, test } from '@playwright/test'
import { login } from './helpers'

// Configuration lives in Postgres: an admin registers a training for a team and gives its roles to a group and to a
// person; the person joins the team and its leader retags them, all in the app with no YAML anywhere. Idempotent on a
// KEEP=1 stack: the newcomer's address carries a run id, and every step tolerates what an earlier run left behind.
const run = Date.now().toString(36)

test('an admin registers a training for a team and gives its roles to groups and people; a leader retags; the trainee sees it', async ({ browser }) => {
  const admin = await login(browser, 'admin')
  await admin.getByRole('button', { name: 'Administrator' }).click()
  await admin.getByRole('menuitem', { name: 'Trainings' }).click()
  await expect(admin.getByRole('heading', { name: 'Manage trainings' })).toBeVisible()
  const list = admin.getByRole('navigation', { name: 'Trainings' })
  if (!(await list.getByRole('link', { name: /forge-001/ }).count())) {
    await admin.getByRole('link', { name: '+ New training' }).click()
    await admin.getByLabel('Training id').fill('forge-001')
    await admin.getByLabel('Team that owns it').selectOption('forge')
    await admin.getByLabel('Repository URL').fill('file:///git/forge-001.git') // seeded by scripts/seed-git.sh
    await admin.getByRole('button', { name: 'Register training' }).click()
  } else {
    await list.getByRole('link', { name: /forge-001/ }).click()
  }
  await expect(admin.getByRole('heading', { name: 'Forge 001: Striking the Spark' })).toBeVisible()
  await expect(admin.getByText('file:///git/forge-001.git')).toBeVisible() // its managers see the source

  // Every trainee of The Forge takes it, through a group; so does someone on no team yet, by email.
  const roles = admin.getByRole('region', { name: 'Roles in Forge 001: Striking the Spark' })
  const newcomer = `newcomer-${run}@crucible.local`
  for (const who of ['forge_trainees', newcomer]) {
    const chip = roles.getByRole('button', { name: `Remove ${who} from participants` })
    if (!(await chip.count())) {
      await roles.getByLabel('Add to participants').fill(who)
      await roles.getByLabel('Add to participants').press('Enter')
    }
    await expect(chip).toBeVisible()
  }
  // A role given and taken back.
  await roles.getByLabel('Add to admins').fill('all_seniors')
  await roles.getByLabel('Add to admins').press('Enter')
  const seniors = roles.getByRole('button', { name: 'Remove all_seniors from admins' })
  await expect(seniors).toBeVisible()
  await seniors.click()
  await expect(seniors).toHaveCount(0)

  // An admin adds the newcomer to The Forge as a trainee.
  await admin.goto('/teams/forge')
  await admin.getByLabel('Person to add').fill(newcomer)
  await admin.getByLabel('Their tag', { exact: true }).selectOption('trainee') // each person's tag button says "Change their tag"
  await admin.getByRole('button', { name: 'Add to team' }).click()
  await expect(admin.getByRole('button', { name: `${newcomer} is a trainee. Change their tag` })).toBeVisible()
  await expect(admin.getByRole('link', { name: 'Manage' }).first()).toBeVisible()

  // The leader finds the newcomer in the team's list and makes them a junior, choosing the tag in place.
  const leader = await login(browser, 'leader')
  await leader.goto('/teams/forge')
  await leader.getByRole('button', { name: `${newcomer} is a trainee. Change their tag` }).click()
  await leader.getByRole('menuitemradio', { name: 'junior' }).click()
  await expect(leader.getByRole('button', { name: `${newcomer} is a junior. Change their tag` })).toBeVisible()
  await expect(leader.getByRole('button', { name: 'Add to team' })).toHaveCount(0) // only admins add people

  // The trainee finds the new training on the Hearth and reads it.
  const t = await login(browser, 'trainee')
  await t.getByRole('link', { name: /Forge 001/ }).click()
  await t.getByTestId('module-01-spark').getByRole('link').first().click()
  await expect(t.getByText('Every blade starts as a spark.')).toBeVisible()
})
