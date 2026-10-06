import { expect, test } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { Timer } from './Timer'
import type { LabView } from '../types'

test('the timer says when an extension is waiting for approval', () => {
  const lab = { id: 'a', state: 'ready', ends_at: new Date(Date.now() + 3_600_000).toISOString(), server_now: new Date().toISOString(),
    can_extend: false, extension_pending: true, limit_reason: 'ttl' } as unknown as LabView
  const html = renderToStaticMarkup(<Timer lab={lab} offset={0} onExtend={() => {}} />)
  expect(html).toContain('Extension pending')
  expect(html).not.toContain('>Extend<')
})
