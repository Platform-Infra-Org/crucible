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

const { AdminReposPage, Kinds } = await import('./AdminRepos')
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

  // Test connection also tries a push (a dry run), so an admin sees whether edits can reach the repository, even one
  // connected with no credential that the instance's own git credentials can push to.
  test('says whether Crucible can write once it can read', () => {
    view = { repos: [repo({ status: 'ok', can_write: true }), repo({ id: 2, url: 'https://github.com/org/b.git', status: 'ok', can_write: false })], can_keep_secrets: true }
    const out = html()
    expect(out).toContain('Can write')
    expect(out).toContain('Read only')
    view = { repos: [repo({ status: 'unknown', can_write: null })], can_keep_secrets: true }
    expect(html()).not.toMatch(/Can write|Read only/)
  })

  // No credential is not a promise of read-only: git may still use the instance's own credentials for an https repo.
  test('the no-credential choice says what Crucible adds, not that nothing can write', () => {
    const out = renderToStaticMarkup(<Kinds url="https://github.com/org/t.git" auth="none" setAuth={() => {}} canKeepSecrets />)
    expect(out).toContain('No credential of its own')
    expect(out).toContain('git credentials')
    expect(out).not.toContain('the repository is public')
  })

  test('without a secret key the page says so', () => {
    view = { repos: [], can_keep_secrets: false }
    const out = html()
    expect(out).toContain('CRUCIBLE_SECRET_KEY')
    expect(out).toContain('No repositories yet')
  })

  // The form to connect one lives in a dialog, so the page itself is the list plus the way in.
  test('the way to connect one is a button, not a form on the page', () => {
    view = { repos: [repo()], can_keep_secrets: true }
    const out = html()
    expect(out).toContain('Connect a repository')
    expect(out).not.toContain('Repository URL') // only inside the dialog, which is closed
  })
})
