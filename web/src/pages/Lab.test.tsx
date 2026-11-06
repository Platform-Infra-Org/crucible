import { beforeEach, expect, test, vi } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router'
import { AsleepLab, LabPage, LabWorkspace } from './Lab'
import type { LabView, ModuleLab } from '../types'

const clusterInfo = { title: 'Cluster heat', runtime: 'cluster', runtime_ready: false, runtime_message: 'Your laptop agent is offline.', lab: null, contexts: [] } as unknown as ModuleLab
let info: ModuleLab
beforeEach(() => { info = clusterInfo })
vi.mock('../useFetch', () => ({ useFetch: () => ({ data: info, error: undefined }) }))
vi.mock('../lib/alerts', () => ({ toast: () => {}, askNotifications: () => {}, notificationsUndecided: () => false }))

const page = () => renderToStaticMarkup(<MemoryRouter><LabPage /></MemoryRouter>)

test('a cluster lab waiting on the agent links to Connect your laptop', () => {
  const h = page()
  expect(h).toContain('Your laptop agent is offline.')
  expect(h).toContain('href="/connect"')
  expect(h).not.toContain('How this estimate was worked out')
})

test('an aws lab shows how its estimate was worked out, resource by resource', () => {
  info = {
    title: 'Cloud heat', runtime: 'aws', runtime_ready: true, lab: null, estimate_usd: 0.25, needs_approval: true,
    estimate_breakdown: {
      hourly_usd: 0.0753, lines: [
        { address: 'aws_instance.web', type: 'aws_instance', hourly_usd: 0.0116, usage_hourly_usd: 0 },
        { address: 'aws_nat_gateway.out', type: 'aws_nat_gateway', hourly_usd: 0.05, usage_hourly_usd: 0.0137, note: 'declared' },
        { address: 'aws_s3_bucket.logs', type: 'aws_s3_bucket', hourly_usd: 0, usage_hourly_usd: 0, note: 'usage not stated' },
        { address: 'aws_iam_role.lab', type: 'aws_iam_role', hourly_usd: 0, usage_hourly_usd: 0, note: 'free' },
      ],
    },
  } as unknown as ModuleLab
  const h = page()
  expect(h).toMatch(/<details class="lab-settings[^"]*"><summary>How this estimate was worked out<\/summary>/)
  expect(h).toContain('<td><code>aws_instance.web</code></td><td>aws_instance</td><td>$0.0116</td><td>—</td><td></td>')
  expect(h).toContain('<td>$0.0500</td><td>$0.0137</td><td>declared</td>')
  expect(h).toContain('usage not stated')
  expect(h).toContain('<td>free</td>')
  expect(h).toContain('$0.0753')
})

const workspace = (runtime: string, state: string) => renderToStaticMarkup(
  <MemoryRouter><LabWorkspace lab={{
    id: 'l1', state, runtime, log: [], ends_at: new Date(Date.now() + 3600e3).toISOString(),
    tasks: [{ id: 't1', status: 'open', title: 'T', body: '' }], terminals: [{ name: 'main' }],
  } as unknown as LabView} setLab={() => {}} title="Heat" back="/" /></MemoryRouter>)

test('only a ready aws lab has an AWS console button', () => {
  expect(workspace('aws', 'ready')).toContain('AWS console')
  expect(workspace('cluster', 'ready')).not.toContain('AWS console')
  expect(workspace('aws', 'destroying')).not.toContain('AWS console')
})

// LabPage loads its lab in an effect, which a static render never runs: render the asleep view itself.
const asleep = (slept: 'hibernated' | 'stopped', error?: string) => renderToStaticMarkup(
  <MemoryRouter><AsleepLab lab={{ id: 'l2', state: 'asleep', runtime: 'aws', slept_as: slept, slept_at: new Date().toISOString(), error,
    ends_at: new Date(Date.now() + 3600e3).toISOString(), tasks: [], terminals: [] } as unknown as LabView}
  setLab={() => {}} title="Cloud heat" back="/" /></MemoryRouter>)

test('a sleeping lab says how it slept and offers Wake it', () => {
  let h = asleep('hibernated')
  expect(h).toContain('Your lab is asleep')
  expect(h).toContain('Crucible hibernated your machines last night: they resume where you left them.')
  expect(h).toMatch(/<button[^>]*class="primary"[^>]*>Wake it<\/button>/)
  h = asleep('stopped', "starting the lab's machines: InsufficientInstanceCapacity")
  expect(h).toContain('Crucible stopped your machines last night: they restart from their disks.')
  expect(h).toContain('InsufficientInstanceCapacity')
})

test('after a wake the workspace says once what came back', () => {
  const woke = (slept: string) => renderToStaticMarkup(
    <MemoryRouter><LabWorkspace lab={{
      id: 'l3', state: 'ready', runtime: 'aws', slept_as: slept, woke_at: new Date().toISOString(), log: [],
      ends_at: new Date(Date.now() + 3600e3).toISOString(), tasks: [{ id: 't1', status: 'open', title: 'T', body: '' }], terminals: [{ name: 'main' }],
    } as unknown as LabView} setLab={() => {}} title="Heat" back="/" /></MemoryRouter>)
  expect(woke('hibernated')).toContain('Your machines resumed where you left them.')
  expect(woke('stopped')).toContain("Your machines restarted from their disks; anything you hadn&#x27;t saved is gone.")
})

test('a workspace over its limit is warned', () => {
  const h = renderToStaticMarkup(<MemoryRouter><LabWorkspace lab={{
    id: 'l4', state: 'ready', runtime: 'aws', workspace_warning: true, log: [],
    ends_at: new Date(Date.now() + 3600e3).toISOString(), tasks: [{ id: 't1', status: 'open', title: 'T', body: '' }], terminals: [{ name: 'main' }],
  } as unknown as LabView} setLab={() => {}} title="Heat" back="/" /></MemoryRouter>)
  expect(h).toContain('Your workspace holds more than 20 GiB')
})
