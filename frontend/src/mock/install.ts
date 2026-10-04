// Made-up PCs, games and backups for `npm run dev:mock`: the interface runs in a
// normal browser without the Go side, for design work, screenshots and GIFs.
// It stands in for Wails' window.go and window.runtime, so the views and the
// generated bindings stay exactly as they are in the app.
import { backup, conflict, main, store } from '../../wailsjs/go/models'

const MB = 1024 ** 2
const KB = 1024
const minute = 60_000

type Handler = (...data: unknown[]) => void
const listeners = new Map<string, Set<Handler>>()

function emit(name: string, ...data: unknown[]) {
  listeners.get(name)?.forEach(cb => cb(...data))
}

function on(name: string, cb: Handler): () => void {
  let set = listeners.get(name)
  if (!set) listeners.set(name, (set = new Set()))
  set.add(cb)
  return () => set.delete(cb)
}

// A model's fields without its methods, nested models included, so typos in
// the made-up data fail `npm run check`.
type Plain<T> = T extends (infer U)[] ? Plain<U>[] : T extends object ? { [K in keyof T as K extends 'convertValues' ? never : K]?: Plain<T[K]> } : T

const wait = (ms: number) => new Promise(r => setTimeout(r, ms))

export function installMock() {
  // Read once, so ?scheme pins and Playwright's fixed clock give the same pictures every run.
  const now = Date.now()
  const iso = (minutesAgo: number) => new Date(now - minutesAgo * minute).toISOString()
  const params = new URLSearchParams(location.search)
  const me = 'Desktop'

  // [title, save folder under the user's profile, size, files, minutes since last change, restore points]
  const games: [string, string, number, number, number, number][] = [
    ['Ember Crown', 'Saved Games\\Ember Crown', 18 * MB, 42, 14, 6],
    ['Frostline', 'AppData\\Local\\Frostline\\Saved', 64 * MB, 118, 52, 9],
    ['Grimwald', 'Documents\\My Games\\Grimwald', 9 * MB, 27, 130, 5],
    ['Hollow Tide', 'AppData\\Roaming\\HollowTide', 3.4 * MB, 12, 240, 8],
    ['Iron Veil', 'Saved Games\\Iron Veil', 41 * MB, 64, 3, 12],
    ['Lumen Drift', 'AppData\\LocalLow\\Driftworks\\Lumen Drift', 2.1 * MB, 9, 380, 4],
    ['Neon Meridian', 'Documents\\Neon Meridian\\Profiles', 27 * MB, 55, 95, 7],
    ['Quiet Harbor', 'AppData\\Local\\QuietHarbor\\Saves', 1.2 * MB, 6, 600, 3],
    ['Sable Run', 'Documents\\Sable Run', 12 * MB, 31, 31, 10],
    ['Starfall Protocol', 'AppData\\Local\\Starfall\\Saved\\SaveGames', 88 * MB, 203, 22, 11],
    ['Tidebreaker', 'Saved Games\\Tidebreaker', 15 * MB, 38, 1500, 2],
    ['Wicker & Ash', 'Documents\\Wicker and Ash', 5.6 * MB, 19, 2800, 6],
  ]

  const folders: main.FolderView[] = games.map(([label, rel, bytes, files, changed, points], i) =>
    main.FolderView.createFrom({
      id: `g${i}`, label, game: label, path: `C:\\Users\\Alex\\${rel}`, state: 'idle', bytes, files, points,
      needBytes: 0, errors: 0, problem: '', backup: true, sync: true, installed: true, exists: true, shared: 2,
      conflicts: label === 'Hollow Tide' ? 1 : 0, modified: iso(changed), backedUp: iso(Math.min(changed + 20, 55)),
      backupBytes: Math.round(bytes * 1.6), exclude: [], newerAt: null,
    } satisfies Plain<main.FolderView>),
  )

  let settings = store.Settings.createFrom({
    theme: params.get('theme') ?? '', backupEnabled: true, backupRoot: '', driveRoot: '', backupBackend: 'google',
    intervalHours: 6, keepDays: 30, noBackup: {}, ignored: {}, showSteamCloud: false, autoAdd: true, autoAddMaxGB: 2,
    dismissed: {}, closeToTray: true, startAtLogin: true, migrated: true, pauseWhileGaming: true, installedOnly: true,
    syncDisabled: false, backupOnly: {}, pausedUntil: null, notify: true, noUpdateCheck: false, noAutoUpdate: false,
    noCloudPull: false, noHoldWhilePlaying: false, exclude: {}, findMods: false, autoAddMods: false,
    syncDeployedMods: false, shareVortexMods: false, modsMaxGB: 5, modsChanged: null, mods: {}, accounts: false, launchers: {},
  } satisfies Plain<store.Settings>)

  const myID = 'K7QPXAB-2NFD4ZL-W3H6YRC-9TMUE5J-QGV8S1D-LXA4OBC-HN2WKPE-R6TY7ZM'
  let lastBackup = store.BackupRun.createFrom({
    started: iso(15), finished: iso(14), ok: true, folders: folders.length, copied: 23, versions: 118,
    bytes: 183 * MB, errors: [], held: [], target: 'Google Drive', notPruned: '',
  } satisfies Plain<store.BackupRun>)
  let backingUp = false

  const overview = () => main.Overview.createFrom({
    syncthing: { installed: true, running: true, myID, gui: 'http://127.0.0.1:8384', error: '' },
    devices: 2, online: 2, pending: 0, folders: folders.length, syncing: 0, errors: 0, issues: [],
    conflicts: folders.reduce((n, f) => n + f.conflicts, 0), overlaps: 0,
    drive: { found: false, running: false, myDrive: '', drives: [] },
    google: { available: true, signedIn: true, account: 'alex@example.com', synced: lastBackup.finished, error: '' },
    target: 'C:\\Users\\Alex\\AppData\\Local\\Syncer\\GameSaveBackup', lastBackup, backingUp, gaming: false, paused: false,
    settings, version: 'v1.0.0', versionGaps: [],
  } satisfies Plain<main.Overview>)

  const devices = main.DevicesView.createFrom({
    myID, myName: me, qr: '', pending: [],
    devices: [
      { id: 'LP4XQ2M-ABCDEF1-GHIJKL2-MNOPQR3-STUVWX4-YZ12345-6789ABC-DEFGHJK', name: 'Laptop', connected: true, address: '192.168.1.24:22000', via: 'lan', completion: 100, needBytes: 0, version: 'v1.0.0', versionGap: '' },
      { id: 'LR9ZYT3-KLMNOP1-QRSTUV2-WXYZAB3-CDEFGH4-JKLMNP5-QRSTUV6-WXYZ234', name: 'Living-room PC', connected: true, address: '192.168.1.31:22000', via: 'lan', completion: 100, needBytes: 0, version: 'v1.0.0', versionGap: '' },
    ],
  } satisfies Plain<main.DevicesView>)

  // Games on this PC that aren't synced yet, for the "found" list.
  const found = [
    main.GameView.createFrom({ name: 'Copper Fields', path: 'C:\\Users\\Alex\\Documents\\Copper Fields\\Saves', known: true, size: 6 * MB, files: 14, modified: iso(4000), syncedBy: '', installed: true, dismissed: false } satisfies Plain<main.GameView>),
    main.GameView.createFrom({ name: 'Kestrel', path: 'C:\\Users\\Alex\\AppData\\Roaming\\Kestrel', known: true, size: 900 * KB, files: 3, modified: iso(9000), syncedBy: '', installed: true, dismissed: false } satisfies Plain<main.GameView>),
  ]

  const hoursAgo = [2, 9, 27, 50, 74, 101, 140, 190, 260, 330, 420, 520]
  const pcs = [me, 'Laptop', me, 'Living-room PC', me, 'Laptop', me, me, 'Laptop', me, 'Living-room PC', me]
  const restorePoints = (id: string) => {
    const n = folders.find(f => f.id === id)?.points ?? 0
    const nowS = Math.floor(now / 1000)
    return main.RestorePointsView.createFrom({
      latest: { by: [me], from: [me] },
      points: hoursAgo.slice(0, n).map((h, k) => ({ at: nowS - h * 3600, by: [pcs[k]], from: [pcs[k]] })),
    } satisfies Plain<main.RestorePointsView>)
  }

  const conflicts = (id: string) => folders.find(f => f.id === id)?.conflicts
    ? [conflict.Conflict.createFrom({
        rel: 'slot1.sav', copy: 'slot1.sync-conflict-20261003-084211-LP4XQ2M.sav', device: 'LP4XQ2M', deviceName: 'Laptop',
        size: 412 * KB, modified: iso(260), missing: false, copySize: 398 * KB, copyModified: iso(190), currentName: me,
      } satisfies Plain<conflict.Conflict>)]
    : []

  const changed = () => emit('changed')
  const folder = (id: string) => {
    const f = folders.find(f => f.id === id)
    if (!f) throw new Error(`No folder ${id}`)
    return f
  }

  // Walks through the folders like a real run, about three seconds in all.
  async function runBackup() {
    backingUp = true
    changed()
    let copied = 0
    let sent = 0
    for (const [k, f] of folders.entries()) {
      await wait(240)
      const files = k % 3 === 0 ? 2 : 0
      copied += files
      sent += files ? Math.round(f.bytes / 8) : 0
      emit('backup:progress', { folder: f.label, folderIdx: k + 1, folders: folders.length, filesDone: (k + 1) * 10, copied, bytes: sent })
    }
    await wait(300)
    lastBackup = store.BackupRun.createFrom({ ...lastBackup, started: new Date(Date.now() - 3000).toISOString(), finished: new Date().toISOString(), copied, bytes: sent })
    backingUp = false
    emit('backup:done', lastBackup)
    changed()
  }

  const impl: Partial<Record<string, (...a: any[]) => unknown>> = {
    Overview: overview,
    Folders: () => folders,
    Devices: () => devices,
    Available: () => [],
    ScanGames: () => found,
    ScanMods: () => [],
    Accounts: () => main.AccountsView.createFrom({ enabled: false, active: '', accounts: [], splits: [], waiting: [], op: '', opLabel: '', opError: '', pending: [] }),
    RestorePoints: restorePoints,
    Conflicts: conflicts,
    Decisions: () => [],
    OtherBackups: () => [] as backup.Orphan[],
    ConflictOwners: () => [],
    SaveOwners: () => [],
    Log: () => [`${iso(14)} backup: ${folders.length} folders, 23 files updated`],
    ModSettingsDiffer: () => [],
    VortexShares: () => [],
    CheckForUpdate: () => main.UpdateInfo.createFrom({ latest: '', url: '' }),
    SaveSettings: (s: store.Settings) => ((settings = store.Settings.createFrom(s)), changed(), settings),
    SetFolderSync: (id: string, on: boolean) => { folder(id).sync = on; changed() },
    SetFolderBackup: (id: string, on: boolean) => { folder(id).backup = on; changed() },
    Restore: async (id: string) => { await wait(600); return folder(id).files },
    ResolveConflict: (id: string) => { folder(id).conflicts = 0; changed() },
    ResolveConflicts: (id: string) => { const n = folder(id).conflicts; folder(id).conflicts = 0; changed(); return n },
    BackupNow: () => { if (!backingUp) runBackup() },
    AddFolder: (label: string, path: string) => {
      const g = found.find(g => g.path === path)
      folders.push(main.FolderView.createFrom({ ...folders[0], id: `n${folders.length}`, label, game: label, path, bytes: g?.size ?? 0, files: g?.files ?? 0, points: 0, conflicts: 0 }))
      changed()
    },
  }

  const w = window as any
  // Unknown calls (opening folders, pickers, sign-in) quietly do nothing.
  w.go = { main: { App: new Proxy({}, { get: (_, name: string) => async (...args: unknown[]) => {
    const fn = impl[name]
    const v = fn ? await fn(...args) : null
    return v == null ? v : JSON.parse(JSON.stringify(v))
  } }) } }
  w.runtime = new Proxy({}, { get: (_, name: string) => {
    if (name === 'EventsOnMultiple') return (event: string, cb: Handler) => on(event, cb)
    if (name === 'EventsEmit') return emit
    if (name === 'BrowserOpenURL') return (url: string) => window.open(url, '_blank', 'noopener')
    return () => Promise.resolve(null)
  } })
}
