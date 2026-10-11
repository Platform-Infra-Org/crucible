import { describe, expect, test } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { ClusterPick } from './ClusterPick'
import { ReleaseHealth } from './ReleaseHealth'

describe('cluster labs', () => {
  test('the picker names each context with its server, and a cluster-wide lab asks for consent', () => {
    const h = renderToStaticMarkup(<ClusterPick contexts={[{ name: 'kind-dev', server: '127.0.0.1:52011' }]} value="kind-dev" onChange={() => {}}
      clusterWide consent={false} onConsent={() => {}} />)
    expect(h).toContain('<option value="kind-dev" selected="">kind-dev · 127.0.0.1:52011</option>')
    expect(h).toContain('cluster-wide resources')
    expect(h).toContain('type="checkbox"')
  })
  test('no contexts says how to get one', () => {
    const h = renderToStaticMarkup(<ClusterPick contexts={[]} value="" onChange={() => {}} clusterWide={false} consent onConsent={() => {}} />)
    expect(h).toContain('kind create cluster')
  })
  test('health lists each release and what is not ready, in words', () => {
    const h = renderToStaticMarkup(<ReleaseHealth health={{ releases: [{ name: 'forge', status: 'progressing', unready: ['deployment/forge: 1 of 2 ready'] }] }} />)
    expect(h).toContain('forge')
    expect(h).toContain('Starting')
    expect(h).toContain('deployment/forge: 1 of 2 ready')
  })
})
