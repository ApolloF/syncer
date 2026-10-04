import { describe, expect, it } from 'vitest'
import type { conflict } from '../../wailsjs/go/models'
import { conflictBadge, conflictPCs } from './conflicts'

const copy = (device: string, deviceName = '') => ({ device, deviceName }) as conflict.Conflict

describe('conflictBadge', () => {
  it('calls a single conflict two versions', () => {
    expect(conflictBadge(1)).toBe('2 versions')
    expect(conflictBadge(4)).toBe('4 conflicts')
  })
})

describe('conflictPCs', () => {
  it('lists each PC once, in order', () => {
    expect(conflictPCs([copy('B', 'Laptop'), copy('A', 'Desktop'), copy('B', 'Laptop')])).toEqual([
      { device: 'B', name: 'Laptop' },
      { device: 'A', name: 'Desktop' },
    ])
  })
  it('falls back to the device ID without a name', () => {
    expect(conflictPCs([copy('ABC123')])).toEqual([{ device: 'ABC123', name: 'ABC123' }])
  })
  it('is empty without conflicts', () => {
    expect(conflictPCs([])).toEqual([])
  })
})
