import { expect, test } from 'vitest'
import { initials } from './avatars'

test('initials come from the name, else the email', () => {
  expect(initials({ name: 'Ada Lovelace', email: 'a@x' })).toBe('AL')
  expect(initials({ name: 'Mary Ann Evans', email: 'm@x' })).toBe('ME')
  expect(initials({ name: '', email: 'leo.leader@crucible.local' })).toBe('LL')
  expect(initials({ name: '', email: 'trainee@crucible.local' })).toBe('TR')
})
