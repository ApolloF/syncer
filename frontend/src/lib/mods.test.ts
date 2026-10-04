import { describe, expect, it } from 'vitest'
import { byModKind, modGameName, modPhase } from './mods'

describe('byModKind', () => {
  it('orders staging, profiles, deployment, then the rest', () => {
    const kinds = ['other', 'mods-deployed', undefined, 'mods', 'mods-profiles']
    expect(kinds.map(kind => ({ kind })).sort(byModKind).map(f => f.kind))
      .toEqual(['mods', 'mods-profiles', 'mods-deployed', 'other', undefined])
  })
})

describe('modGameName', () => {
  it('strips the folder suffix', () => {
    expect(modGameName('Skyrim Special Edition (Vortex mods)')).toBe('Skyrim Special Edition')
    expect(modGameName('Fallout 4 (Vortex load order)')).toBe('Fallout 4')
    expect(modGameName('Fallout 4 (deployed mods)')).toBe('Fallout 4')
  })
  it('keeps other parentheses', () => {
    expect(modGameName('Doom (1993)')).toBe('Doom (1993)')
  })
})

describe('modPhase', () => {
  it('shows a source as sending unless held or busy', () => {
    expect(modPhase({ modRole: 'source', modPhase: '' })).toEqual({ kind: 'ok', text: 'Sending' })
    expect(modPhase({ modRole: 'source', modPhase: 'busy' }).text).toBe('Vortex or game open')
    expect(modPhase({ modRole: 'source', modPhase: 'held' }).kind).toBe('err')
  })
  it('shows where a receiving PC is with an update', () => {
    expect(modPhase({ modRole: '', modPhase: 'pending' }).text).toBe('Update waiting')
    expect(modPhase({ modRole: '', modPhase: 'applying' }).text).toBe('Applying…')
    expect(modPhase({ modRole: '', modPhase: 'held' }).text).toBe('On hold')
    expect(modPhase({ modRole: '', modPhase: '' })).toEqual({ kind: 'ok', text: 'Up to date' })
  })
  it('does not treat a receiver as busy', () => {
    expect(modPhase({ modRole: '', modPhase: 'busy' }).text).toBe('Up to date')
  })
})
