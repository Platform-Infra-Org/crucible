import { describe, expect, test } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router'
import { AdminMenu, adminItems } from './AdminMenu'

const at = (path: string) => renderToStaticMarkup(<MemoryRouter initialEntries={[path]}><AdminMenu /></MemoryRouter>)

describe('Administrator menu', () => {
  test('is a closed menu button, with the admin pages hidden until it is opened', () => {
    const html = at('/')
    expect(html).toContain('Administrator')
    expect(html).toContain('aria-haspopup="menu"')
    expect(html).toContain('aria-expanded="false"')
    expect(html).not.toContain('href="/admin/settings"')
    expect(html).not.toContain('href="/trainings/manage"')
  })

  test('stays marked while any admin page is open, so you can see where you are', () => {
    expect(at('/admin/settings')).toContain('admin-menu-button active')
    expect(at('/trainings/manage/forge-101')).toContain('admin-menu-button active')
    expect(at('/admin')).toContain('admin-menu-button active')
    expect(at('/labs')).not.toContain('admin-menu-button active')
  })

  test('holds Forge Status, Forge settings and Trainings', () => {
    expect(adminItems.map((i) => i.label)).toEqual(['Forge Status', 'Forge settings', 'Trainings'])
    expect(at('/trainings')).not.toContain('admin-menu-button active') // the catalog is everyone's page
  })
})
