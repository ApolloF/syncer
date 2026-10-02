import type { main } from '../../wailsjs/go/models'
import { bytes, ago } from './fmt'

/** A time from Go; the zero time means "never". */
export const isTime = (t: any) => !!t && new Date(t).getFullYear() > 2000

export const plural = (n: number, w: string) => `${n} ${w}${n === 1 ? '' : 's'}`

export function backupLine(f: main.FolderView): string {
  const parts = [isTime(f.backedUp) ? `Backed up ${ago(f.backedUp)}` : 'Not backed up yet']
  if (f.backupBytes) parts.push(bytes(f.backupBytes))
  if (f.points) parts.push(plural(f.points, 'restore point'))
  return parts.join(' · ')
}

/** A synced folder's state as a pill. */
export function stateOf(f: main.FolderView): { kind: string; text: string } {
  // Syncthing stopped the folder, or some files can't be synced: the row says why.
  if (f.problem) return { kind: 'err', text: f.errors ? `${f.errors} error${f.errors > 1 ? 's' : ''}` : 'Stopped' }
  if (!f.exists) return { kind: 'warn', text: 'Waiting' }
  switch (f.state) {
    case 'idle': return f.needBytes ? { kind: 'accent', text: `${bytes(f.needBytes)} to go` } : { kind: 'ok', text: 'Synced' }
    case 'scanning': case 'scan-waiting': return { kind: '', text: 'Scanning' }
    case 'syncing': case 'sync-preparing': case 'sync-waiting': return { kind: 'accent', text: 'Syncing' }
    case 'paused': return { kind: '', text: 'Paused' }
    case 'error': return { kind: 'err', text: 'Error' }
    default: return { kind: '', text: f.state || '…' }
  }
}
