import { describe, expect, it } from 'vitest'
import { uploadProblem } from './uploads'

describe('uploadProblem', () => {
  const f = (size: number, name = 'a.txt') => ({ name, size })
  it('allows up to five files of 20 MiB', () => {
    expect(uploadProblem([f(1), f(20 << 20), f(3), f(4), f(5)])).toBeNull()
  })
  it('refuses a sixth file', () => {
    expect(uploadProblem([f(1), f(1), f(1), f(1), f(1), f(1)])).toMatch(/At most 5/)
  })
  it('names the file that is too big', () => {
    expect(uploadProblem([f(1), f((20 << 20) + 1, 'huge.zip')])).toBe('huge.zip is larger than 20 MiB.')
  })
})
