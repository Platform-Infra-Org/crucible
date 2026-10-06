import { describe, expect, it } from 'vitest'
import { advanceFocus, isLeaveChord } from './advanceFocus'

const tasks = [{ id: 'a', title: 'First' }, { id: 'b', title: 'Second' }]
describe('advanceFocus', () => {
  it('targets the next task pip and announces it', () => {
    expect(advanceFocus(tasks, 'a', 'b')).toEqual({ index: 1, message: 'Task 2 of 2 is next' })
  })
  it('does nothing when there is no next task or it is unknown', () => {
    expect(advanceFocus(tasks, 'b', 'b')).toBeNull()
    expect(advanceFocus(tasks, 'a', 'zz')).toBeNull()
  })
})
describe('isLeaveChord', () => {
  const k = (o: object) => ({ ctrlKey: false, altKey: false, shiftKey: false, key: '', ...o })
  it('accepts Ctrl+Alt+Up and Ctrl+Shift+F6 only', () => {
    expect(isLeaveChord(k({ ctrlKey: true, altKey: true, key: 'ArrowUp' }))).toBe(true)
    expect(isLeaveChord(k({ ctrlKey: true, shiftKey: true, key: 'F6' }))).toBe(true)
    expect(isLeaveChord(k({ ctrlKey: true, key: 'ArrowUp' }))).toBe(false)
    expect(isLeaveChord(k({ key: 'F6', shiftKey: true }))).toBe(false)
  })
})
