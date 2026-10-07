import { describe, expect, test, vi } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router'
import { AdminMenu, adminItems } from './AdminMenu'

vi.mock('../me', () => ({ useMe: () => ({ me: { config_in_db: true } }) }))

const at = (path: string) => renderToStaticMarkup(<MemoryRouter initialEntries={[path]}><AdminMenu /></MemoryRouter>)

describe('Administrator menu', () => {
  test('is a closed menu button, with the admin pages hidden until it is opened', () => {
    const html = at('/')
    expect(html).toContain('Administrator')
    expect(html).toContain('aria-haspopup="menu"')
    expect(html).toContain('aria-expanded="false"')
    expect(html).not.toContain('href="/admin/settings"')
    expect(html).not.toContain('href="/admin/trainings"')
  })

  test('stays marked while any admin page is open, so you can see where you are', () => {
    expect(at('/admin/settings')).toContain('admin-menu-button active')
    expect(at('/admin/trainings')).toContain('admin-menu-button active')
    expect(at('/admin')).toContain('admin-menu-button active')
    expect(at('/labs')).not.toContain('admin-menu-button active')
  })

  test('Settings and Registry appear only when the configuration is in Postgres; Forge Status always', () => {
    expect(adminItems(false).map((i) => i.label)).toEqual(['Forge Status'])
    expect(adminItems(true).map((i) => i.label)).toEqual(['Forge Status', 'Forge settings', 'Registry'])
  })
})
