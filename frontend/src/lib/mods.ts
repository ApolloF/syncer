import type { main } from '../../wailsjs/go/models'

// A game's mod folders in the order Vortex uses them: staging, profiles, deployment.
const kindOrder: Record<string, number> = { 'mods': 0, 'mods-profiles': 1, 'mods-deployed': 2 }

/** Sorts mod folders by kind; unknown kinds go last. */
export const byModKind = (a: { kind?: string }, b: { kind?: string }) =>
  (kindOrder[a.kind ?? ''] ?? 9) - (kindOrder[b.kind ?? ''] ?? 9)

/** The game a mod folder belongs to, from its label ("Skyrim (Vortex mods)" → "Skyrim"). */
export const modGameName = (label: string) => label.replace(/ \((Vortex mods|Vortex load order|deployed mods)\)$/, '')

/** A deployed-mods folder's state as a pill. */
export function modPhase(f: Pick<main.FolderView, 'modRole' | 'modPhase'>): { kind: string; text: string } {
  if (f.modRole === 'source') {
    if (f.modPhase === 'held') return { kind: 'err', text: 'On hold' }
    if (f.modPhase === 'busy') return { kind: 'accent', text: 'Vortex or game open' }
    return { kind: 'ok', text: 'Sending' }
  }
  switch (f.modPhase) {
    case 'pending': return { kind: 'accent', text: 'Update waiting' }
    case 'applying': return { kind: 'accent', text: 'Applying…' }
    case 'held': return { kind: 'err', text: 'On hold' }
    default: return { kind: 'ok', text: 'Up to date' }
  }
}
