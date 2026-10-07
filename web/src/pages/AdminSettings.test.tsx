import { describe, expect, test, vi } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router'
import { AdminSettingsPage } from './AdminSettings'
import { AdminTrainingsPage } from './AdminTrainings'
import { Conflict } from '../components/Conflict'
import { repoWarning, tiersRequest } from '../lib/adminConfig'

let tiers: unknown = null
let admins = ['a@x']
vi.mock('../useFetch', () => ({
  useFetch: (path: string) => {
    const data: Record<string, unknown> = {
      '/api/admin/settings': { version: 1, default_theme: 'forge', cost_tiers: tiers, cluster_usd_per_hour: null, escalation_hours: 4, ranks: { ingot: 20, tempered: 45, blade: 75, sword: 90, masterwork: 100 } },
      '/api/admin/admins': { admins },
      '/api/admin/platform': { schedules: {}, programs: [{ team: 't', training: 'forge-101' }, { team: 'u', training: 'forge-101' }] },
      '/api/meta': { quotes: [] },
      '/api/admin/trainings': { trainings: [{ id: 'forge-101', repo: 'https://git.example/old.git', branch: 'main' }] },
    }
    return { data: data[path], error: undefined, reload: () => {} }
  },
}))
const page = () => renderToStaticMarkup(<MemoryRouter><AdminSettingsPage /></MemoryRouter>)

describe('tiers', () => {
  test('out of order is refused and names the problem', () => {
    const r = tiersRequest('50', '5', '20') // the page sends nothing when a problem comes back
    expect(r.tiers).toBeNull()
    expect(r.problem).toMatch(/auto-approve.*cannot be more than/i)
  })
  test('in order, and all-empty (unset), are accepted', () => {
    expect(tiersRequest('1', '5', '20').tiers).toEqual({ auto_approve_usd: 1, tier1_usd: 5, tier2_usd: 20 })
    expect(tiersRequest('', '', '')).toEqual({ tiers: null })
  })
})

describe('banners', () => {
  test('cost tiers banner shows while unset and goes once set', () => {
    tiers = null
    expect(page()).toContain('No cost tiers are set, so lab requests that cost money can')
    tiers = { auto_approve_usd: 1, tier1_usd: 5, tier2_usd: 20 }
    expect(page()).not.toContain('No cost tiers are set')
  })
  test('single-admin warning shows for one admin, not two', () => {
    admins = ['a@x']
    expect(page()).toContain('losing this account locks the forge')
    admins = ['a@x', 'b@x']
    expect(page()).not.toContain('locks the forge')
  })
})

describe('repoint', () => {
  test('a repo change warns with the program count; a branch-only change does not', () => {
    expect(repoWarning('old', 'new', 2)).toMatch(/^2 programs are pinned/)
    expect(repoWarning('old', 'old', 2)).toBe('')
    expect(repoWarning(undefined, 'new', 2)).toBe('') // a new registration
  })
  test('the registry page starts without a warning', () => {
    expect(renderToStaticMarkup(<AdminTrainingsPage />)).not.toContain('pinned to content')
  })
})

test('a 409 offers a reload', () => {
  const h = renderToStaticMarkup(<Conflict onReload={() => {}} message="Someone changed this, reload to see the latest." />)
  expect(h).toContain('Someone changed this, reload')
  expect(h).toContain('Reload')
})
