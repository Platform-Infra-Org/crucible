import { describe, expect, test, vi } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router'
import { TrainingsPage } from './Trainings'
import type { CatalogEntry } from '../types'

let data: CatalogEntry[] = []
const me = { can_manage_trainings: false, can_edit_content: false, is_admin: false, teams: ['forge'] }
vi.mock('../useFetch', () => ({ useFetch: () => ({ data, error: undefined }) }))
vi.mock('../me', () => ({ useMe: () => ({ me }) }))

const entry: CatalogEntry = { id: 'forge-101', title: 'Forge 101', description: '', estimated_hours: 2, modules: 3, enrolled: [{ id: 'forge', name: 'Forge' }], available: true }
const html = () => renderToStaticMarkup(<MemoryRouter><TrainingsPage /></MemoryRouter>)

describe('Trainings catalog', () => {
  test('Manage trainings shows only for those who can manage them', () => {
    data = [entry]
    expect(html()).not.toContain('Manage trainings')
    me.can_manage_trainings = true
    expect(html()).toContain('href="/trainings/manage"')
    me.can_manage_trainings = false
  })
  test('an author reaches the editor through Manage trainings, or the Edits list when they are on no team', () => {
    data = [entry]
    me.can_edit_content = true
    expect(html()).toContain('href="/trainings/manage"')
    me.teams = []
    expect(html()).toContain('href="/edits"')
    me.can_edit_content = false
    me.teams = ['forge']
  })
  test('a card opens the training on Manage trainings for those who manage, and stays plain for trainees', () => {
    data = [entry]
    expect(html()).not.toContain('href="/trainings/manage/forge-101"')
    me.can_manage_trainings = true
    const h = html()
    me.can_manage_trainings = false
    expect(h).toContain('href="/trainings/manage/forge-101"')
    expect(h).toContain('href="/p/forge/forge-101"') // the Open link still works inside the card
  })
  test('empty state', () => {
    data = []
    expect(html()).toContain('No trainings are available yet.')
  })
  test('an available training links to its program', () => {
    data = [entry]
    expect(html()).toContain('href="/p/forge/forge-101"')
  })
  test('unavailable content shows a notice and no link', () => {
    data = [{ ...entry, available: false }]
    const h = html()
    expect(h).toContain('Content unavailable right now')
    expect(h).not.toContain('href=')
  })
})
