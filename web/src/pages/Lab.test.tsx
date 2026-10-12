import { expect, test, vi } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router'
import { LabPage } from './Lab'
import type { ModuleLab } from '../types'

let info = { title: 'Cluster heat', runtime: 'cluster', runtime_ready: false, runtime_message: 'Your laptop agent is offline.', lab: null, contexts: [] } as unknown as ModuleLab
vi.mock('../useFetch', () => ({ useFetch: () => ({ data: info, error: undefined }) }))

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
