import type { conflict } from '../../wailsjs/go/models'

/** A folder's conflict count as a pill: one conflict is two versions of a save. */
export const conflictBadge = (n: number) => (n === 1 ? '2 versions' : `${n} conflicts`)

/** The other PCs that made conflict copies, once each and in the order first seen. */
export function conflictPCs(cs: conflict.Conflict[]): { device: string; name: string }[] {
  const m = new Map<string, string>()
  for (const c of cs) if (!m.has(c.device)) m.set(c.device, c.deviceName || c.device)
  return [...m.entries()].map(([device, name]) => ({ device, name }))
}
