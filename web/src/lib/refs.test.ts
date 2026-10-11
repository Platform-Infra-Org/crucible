import { describe, expect, test } from 'vitest'
import { checkRef, describeGroup, isGroup } from './refs'

describe('role refs', () => {
  test('groups read as words', () => {
    expect(describeGroup('all')).toBe('everyone on every team')
    expect(describeGroup('all_seniors')).toBe('the seniors of every team')
    expect(describeGroup('platform')).toBe('everyone on platform')
    expect(describeGroup('platform_trainees')).toBe('the trainees of platform')
    expect(isGroup('platform')).toBe(true)
    expect(isGroup('a@x')).toBe(false)
  })
  test('a typed ref is an email or a known group', () => {
    const groups = ['all', 'all_seniors', 'platform', 'platform_seniors']
    expect(checkRef(' A@X ', groups)).toEqual({ ref: 'a@x' })
    expect(checkRef('Platform_Seniors', groups)).toEqual({ ref: 'platform_seniors' })
    expect(checkRef('ghost_seniors', groups)).toHaveProperty('problem')
    expect(checkRef('a b@x', groups)).toHaveProperty('problem')
    expect(checkRef('  ', groups)).toHaveProperty('problem')
  })
})
