import { expect, test } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { HeatMap, Legend } from './HeatMap'
import { CalmContext } from '../me'
import type { JourneyRow } from '../types'

const row: JourneyRow = {
  email: 'trainee@crucible.local', name: 'Tara', team: 'forge', training: 'forge-101', title: 'Forge 101: First Heat', percent: 40,
  cells: [
    { module: '01-welcome', title: 'Welcome', heat: 'forged' },
    { module: '02-first-lab', title: 'First Lab', heat: 'glowing' },
    { module: '03-cluster-heat', title: 'Cluster Heat', heat: 'cold' },
  ],
  flags: [{ kind: 'failed_checks', module: '02-first-lab', item: 't1', detail: '3 failed checks on t1' }],
}

test('cells carry their heat in text and aria, flags are notes', () => {
  const html = renderToStaticMarkup(<HeatMap rows={[row]} />)
  expect(html).toContain('aria-label="Welcome: forged"')
  expect(html).toContain('aria-label="First Lab: glowing"')
  expect(html).toContain('aria-label="Cluster Heat: cold"')
  expect(html).toContain('class="heat heat-forged"')
  expect(html).toContain('>●<')
  expect(html).toContain('1 complete, 1 in progress, 1 not started')
  expect(html).toContain('role="note"')
  expect(html).toContain('May be stuck: 3 failed checks on t1')
  expect(html).not.toMatch(/rank #|top /i)
})

test('user text is escaped and calm drops the glow', () => {
  const evil = { ...row, name: '<img src=x onerror=alert(1)>', flags: [{ kind: 'inactive' as const, detail: '<script>x</script>' }] }
  const html = renderToStaticMarkup(<CalmContext value={true}><HeatMap rows={[evil]} /></CalmContext>)
  expect(html).not.toContain('<img')
  expect(html).not.toContain('<script>')
  expect(html).toContain('heat-cells calm')
})

test('legend names every heat in words', () => {
  const html = renderToStaticMarkup(<Legend />)
  expect(html).toMatch(/cold \(not started\).*glowing \(in progress\).*forged \(complete\)/)
})

test('empty state', () => {
  expect(renderToStaticMarkup(<HeatMap rows={[]} />)).toContain('Nobody to show')
})
