import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { main } from '../../wailsjs/go/models'
import { backupLine, isTime, plural, stateOf } from './folders'

const folder = (f: Partial<main.FolderView>) => ({ exists: true, state: 'idle', ...f }) as main.FolderView

describe('isTime', () => {
  it('rejects Go zero times', () => {
    expect(isTime('0001-01-01T00:00:00Z')).toBe(false)
    expect(isTime('')).toBe(false)
    expect(isTime('2026-10-01T08:00:00Z')).toBe(true)
  })
})

describe('plural', () => {
  it('adds an s except for one', () => {
    expect(plural(0, 'file')).toBe('0 files')
    expect(plural(1, 'file')).toBe('1 file')
    expect(plural(2, 'file')).toBe('2 files')
  })
})

describe('backupLine', () => {
  beforeEach(() => { vi.useFakeTimers(); vi.setSystemTime(new Date('2026-10-03T12:00:00Z')) })
  afterEach(() => { vi.useRealTimers() })

  it('says when a folder was never backed up', () => {
    expect(backupLine(folder({ backedUp: '0001-01-01T00:00:00Z' }))).toBe('Not backed up yet')
  })
  it('lists time, size and restore points', () => {
    expect(backupLine(folder({ backedUp: '2026-10-03T10:00:00Z', backupBytes: 3 * 1024 ** 2, points: 1 })))
      .toBe('Backed up 2 h ago · 3.0 MB · 1 restore point')
  })
})

describe('stateOf', () => {
  it('puts problems before everything else', () => {
    expect(stateOf(folder({ problem: 'x', errors: 3, exists: false }))).toEqual({ kind: 'err', text: '3 errors' })
    expect(stateOf(folder({ problem: 'x', errors: 1 }))).toEqual({ kind: 'err', text: '1 error' })
    expect(stateOf(folder({ problem: 'x' }))).toEqual({ kind: 'err', text: 'Stopped' })
  })
  it('waits for a folder that is not there yet', () => {
    expect(stateOf(folder({ exists: false }))).toEqual({ kind: 'warn', text: 'Waiting' })
  })
  it('shows what an idle folder still needs', () => {
    expect(stateOf(folder({}))).toEqual({ kind: 'ok', text: 'Synced' })
    expect(stateOf(folder({ needBytes: 2048 }))).toEqual({ kind: 'accent', text: '2.0 KB to go' })
  })
  it('groups Syncthing states', () => {
    expect(stateOf(folder({ state: 'scan-waiting' })).text).toBe('Scanning')
    expect(stateOf(folder({ state: 'sync-preparing' })).text).toBe('Syncing')
    expect(stateOf(folder({ state: 'error' }))).toEqual({ kind: 'err', text: 'Error' })
  })
  it('passes unknown states through', () => {
    expect(stateOf(folder({ state: 'cleaning' })).text).toBe('cleaning')
    expect(stateOf(folder({ state: '' })).text).toBe('…')
  })
})
