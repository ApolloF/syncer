import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ago, bytes, err, pausedUntil, tomorrowMorning } from './fmt'

describe('bytes', () => {
  it('uses 1024-based units', () => {
    expect(bytes(0)).toBe('0 B')
    expect(bytes(1023)).toBe('1023 B')
    expect(bytes(1536)).toBe('1.5 KB')
    expect(bytes(5 * 1024 ** 3)).toBe('5.0 GB')
  })
  it('drops the decimal from 100 up', () => {
    expect(bytes(150 * 1024 ** 2)).toBe('150 MB')
  })
  it('stops at TB', () => {
    expect(bytes(2048 * 1024 ** 4)).toBe('2048 TB')
  })
})

describe('ago', () => {
  const now = new Date(2026, 9, 3, 12, 0, 0)
  beforeEach(() => { vi.useFakeTimers(); vi.setSystemTime(now) })
  afterEach(() => { vi.useRealTimers() })
  const before = (s: number) => new Date(now.getTime() - s * 1000)

  it('reads Go times, unix seconds and dates alike', () => {
    expect(ago(before(300).toISOString())).toBe('5 min ago')
    expect(ago(before(300).getTime() / 1000)).toBe('5 min ago')
    expect(ago(before(300))).toBe('5 min ago')
  })
  it('steps from seconds to days', () => {
    expect(ago(before(10))).toBe('just now')
    expect(ago(before(3 * 3600))).toBe('3 h ago')
    expect(ago(before(86400))).toBe('1 day ago')
    expect(ago(before(5 * 86400))).toBe('5 days ago')
  })
  it('shows a date after a month', () => {
    const d = before(40 * 86400)
    expect(ago(d)).toBe(d.toLocaleDateString())
  })
  it('treats Go zero times and garbage as never', () => {
    expect(ago('0001-01-01T00:00:00Z')).toBe('never')
    expect(ago('not a date')).toBe('never')
    expect(ago(0)).toBe('never')
    expect(ago(undefined)).toBe('never')
  })
})

describe('pausedUntil', () => {
  const now = new Date(2026, 9, 3, 12, 0, 0)
  beforeEach(() => { vi.useFakeTimers(); vi.setSystemTime(now) })
  afterEach(() => { vi.useRealTimers() })
  const time = (d: Date) => d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })

  it('shows only the time today', () => {
    const d = new Date(2026, 9, 3, 14, 30)
    expect(pausedUntil(d.toISOString())).toBe(time(d))
  })
  it('adds the weekday on another day', () => {
    const d = new Date(2026, 9, 5, 6, 0)
    expect(pausedUntil(d)).toBe(`${d.toLocaleDateString(undefined, { weekday: 'short' })} ${time(d)}`)
  })
  it('is empty without a valid time', () => {
    expect(pausedUntil(null)).toBe('')
    expect(pausedUntil('nope')).toBe('')
  })
})

describe('tomorrowMorning', () => {
  afterEach(() => { vi.useRealTimers() })
  it('is 06:00 the next day, across a month end', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date(2026, 9, 31, 23, 30))
    expect(tomorrowMorning()).toEqual(new Date(2026, 10, 1, 6, 0, 0, 0))
  })
})

describe('err', () => {
  it('gets a message out of whatever was thrown', () => {
    expect(err('plain')).toBe('plain')
    expect(err(new Error('boom'))).toBe('boom')
    expect(err({ message: 404 })).toBe('404')
    expect(err(null)).toBe('null')
  })
})
