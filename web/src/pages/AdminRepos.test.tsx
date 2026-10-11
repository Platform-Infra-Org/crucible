import { describe, expect, test, vi } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router'
import type { Repo, ReposView } from '../types'

const repo = (over: Partial<Repo> = {}): Repo => ({
  id: 1, url: 'https://github.com/org/trainings.git', auth: 'none', username: '', has_secret: false, public_key: '',
  status: 'unknown', can_write: null, last_error: '', checked_at: null, version: 1, trainings: ['kubernetes'], ...over,
})

let view: ReposView = { repos: [repo()], can_keep_secrets: true }
vi.mock('../useFetch', () => ({ useFetch: () => ({ data: view, error: null, reload: () => {} }) }))

const { AdminReposPage } = await import('./AdminRepos')
const html = () => renderToStaticMarkup(<MemoryRouter><AdminReposPage /></MemoryRouter>)

describe('Repositories', () => {
  test('lists a connection with its trainings and whether it has been checked', () => {
    const out = html()
    expect(out).toContain('https://github.com/org/trainings.git')
    expect(out).toContain('kubernetes')
    expect(out).toContain('Not checked yet')
    expect(out).toContain('No credential')
  })

  test('a failed connection shows why', () => {
    view = { repos: [repo({ status: 'failed', last_error: 'fatal: repository not found' })], can_keep_secrets: true }
    const out = html()
    expect(out).toContain('Cannot be read')
    expect(out).toContain('fatal: repository not found')
  })

  // The credential never reaches the browser: the page may say one is set, never what it is.
  test('a stored token is never rendered, only that one is set', () => {
    view = { repos: [repo({ auth: 'token', has_secret: true, username: 'crucible', status: 'ok' })], can_keep_secrets: true }
    const out = html()
    expect(out).toContain('Reachable')
    expect(out).toContain('Token')
    expect(out).toContain('Replace credential')
    expect(out).not.toMatch(/ghp_|glpat-/)
  })

  test('an ssh repository shows the public key to add as a deploy key, never the private one', () => {
    view = {
      repos: [repo({
        url: 'git@github.com:org/trainings.git', auth: 'ssh', has_secret: true, status: 'ok',
        public_key: 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAItest crucible-git@github.com:org/trainings.git',
      })],
      can_keep_secrets: true,
    }
    const out = html()
    expect(out).toContain('Deploy key')
    expect(out).toContain('ssh-ed25519 AAAAC3')
    expect(out).toContain('write access')
    expect(out).not.toContain('PRIVATE KEY')
  })

  test('without a secret key the page says so and offers no token option', () => {
    view = { repos: [], can_keep_secrets: false }
    const out = html()
    expect(out).toContain('CRUCIBLE_SECRET_KEY')
    expect(out).toContain('No repositories yet')
    expect(out).toContain('disabled=""') // the token choice cannot be picked
  })
})
