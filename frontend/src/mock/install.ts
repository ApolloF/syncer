// Made-up PCs, accounts and backups of real games' save folders for `npm run dev:mock`: the interface runs in a
// normal browser without the Go side, for design work, screenshots and GIFs.
// It stands in for Wails' window.go and window.runtime, so the views and the
// generated bindings stay exactly as they are in the app.
import { accounts, backup, conflict, main, mods, store } from '../../wailsjs/go/models'
import type * as App from '../../wailsjs/go/main/App'

const GB = 1024 ** 3
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

// Folder ids the way Syncer makes them (meta.NewID): the label as a slug.
const slug = (label: string) => label.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '')

export function installMock() {
  // Read once, so ?scheme pins and Playwright's fixed clock give the same pictures every run.
  const now = Date.now()
  const iso = (minutesAgo: number) => new Date(now - minutesAgo * minute).toISOString()
  const never = '0001-01-01T00:00:00Z'
  const params = new URLSearchParams(location.search)
  const me = 'Desktop'
  const home = 'C:\\Users\\Alex'

  // [title, save folder under the user's profile, size, files, minutes since last change, restore points]
  const games: [string, string, number, number, number, number][] = [
    ["Baldur's Gate 3", "AppData\\Local\\Larian Studios\\Baldur's Gate 3\\PlayerProfiles",27 * MB, 55, 95, 7],
    ['Black Myth: Wukong', 'AppData\\Local\\b1\\Saved', 64 * MB, 118, 52, 9],
    ['Clair Obscur: Expedition 33', 'AppData\\Local\\Sandfall\\Saved\\SaveGames', 15 * MB, 38, 1500, 2],
    ['Cyberpunk 2077', 'Saved Games\\CD Projekt Red\\Cyberpunk 2077', 18 * MB, 42, 14, 6],
    ['Disco Elysium', 'AppData\\LocalLow\\ZAUM Studio\\Disco Elysium\\SaveGames', 1.2 * MB, 6, 600, 3],
    ['ELDEN RING', 'AppData\\Roaming\\EldenRing', 3.4 * MB, 12, 240, 8],
    ['Hollow Knight: Silksong', 'AppData\\LocalLow\\Team Cherry\\Hollow Knight Silksong', 2.1 * MB, 9, 380, 4],
    ['Indiana Jones and the Great Circle', 'Saved Games\\MachineGames\\TheGreatCircle\\base\\savegame', 9 * MB, 27, 130, 5],
    ['Kingdom Come: Deliverance II', 'Saved Games\\kingdomcome2\\saves', 41 * MB, 64, 3, 12],
    ['Red Dead Redemption 2', 'Documents\\Rockstar Games\\Red Dead Redemption 2\\Profiles', 88 * MB, 203, 22, 11],
    ['Stardew Valley', 'AppData\\Roaming\\StardewValley\\Saves', 12 * MB, 31, 31, 10],
    ['The Witcher 3: Wild Hunt', 'Documents\\The Witcher 3\\gamesaves', 5.6 * MB, 19, 2800, 6],
  ]

  const folders: main.FolderView[] = games.map(([label, rel, bytes, files, changed, points]) =>
    main.FolderView.createFrom({
      id: slug(label), label, game: label, path: `${home}\\${rel}`, state: 'idle', bytes, files, points,
      needBytes: 0, errors: 0, problem: '', backup: true, sync: true, installed: true, exists: true, shared: 2,
      conflicts: label === 'ELDEN RING' ? 1 : 0, modified: iso(changed), backedUp: iso(Math.min(changed + 20, 55)),
      backupBytes: Math.round(bytes * 1.6), exclude: [], newerAt: null, split: false, account: '', kind: '',
    } satisfies Plain<main.FolderView>),
  )

  // ---- accounts: Alex plays here, Sam on the laptop, Mia in the living room ----
  const alex = 'q4alxe', sam = 'p7samd', mia = 'r2miaz'
  const people: accounts.Account[] = [
    accounts.Account.createFrom({ id: alex, name: 'Alex', color: '#3b82f6', created: iso(60 * 24 * 40), updated: iso(60 * 24 * 40) } satisfies Plain<accounts.Account>),
    accounts.Account.createFrom({ id: sam, name: 'Sam', color: '#10b981', created: iso(60 * 24 * 40), updated: iso(60 * 24 * 40) } satisfies Plain<accounts.Account>),
    accounts.Account.createFrom({ id: mia, name: 'Mia', color: '#ec4899', created: iso(60 * 24 * 12), updated: iso(60 * 24 * 12) } satisfies Plain<accounts.Account>),
  ]
  let active = alex
  // Who plays on the other PCs (what they publish).
  const peerActive: Record<string, string> = { 'Laptop': sam, 'Living-room PC': mia }

  // A split game's saves per account: [file, size, minutes since it changed].
  type Save = [string, number, number]
  const splitSaves: Record<string, Record<string, Save[]>> = {
    'black-myth-wukong': {
      [alex]: [
        ['SaveGames/ArchiveSaveFile.1.sav', 21.4 * MB, 52], ['SaveGames/ArchiveSaveFile.2.sav', 20.9 * MB, 1900], ['SaveGames/ArchiveSaveFile.3.sav', 19.6 * MB, 6200],
        ['SaveGames/ArchiveSaveFile.4.sav', 180 * KB, 52], ['SaveGames/ArchiveSaveFile.5.sav', 24 * KB, 52],
        ['Config/Windows/GameUserSettings.ini', 3 * KB, 9000], ['Config/Windows/Input.ini', 2 * KB, 9000],
      ],
      [sam]: [
        ['SaveGames/ArchiveSaveFile.1.sav', 18.2 * MB, 1500], ['SaveGames/ArchiveSaveFile.2.sav', 17.7 * MB, 4300], ['SaveGames/ArchiveSaveFile.4.sav', 170 * KB, 1500],
        ['SaveGames/ArchiveSaveFile.5.sav', 22 * KB, 1500], ['Config/Windows/GameUserSettings.ini', 3 * KB, 4300], ['Config/Windows/Input.ini', 2 * KB, 4300],
      ],
      [mia]: [
        ['SaveGames/ArchiveSaveFile.1.sav', 9.8 * MB, 2900], ['SaveGames/ArchiveSaveFile.4.sav', 150 * KB, 2900], ['SaveGames/ArchiveSaveFile.5.sav', 12 * KB, 2900],
        ['Config/Windows/GameUserSettings.ini', 3 * KB, 2900],
      ],
    },
    'stardew-valley': {
      [alex]: [
        ['Willow_412739561/Willow_412739561', 3.9 * MB, 31], ['Willow_412739561/Willow_412739561_old', 3.8 * MB, 1471], ['Willow_412739561/SaveGameInfo', 64 * KB, 31],
        ['Juniper_398112604/Juniper_398112604', 3.6 * MB, 2400], ['Juniper_398112604/SaveGameInfo', 60 * KB, 2400],
      ],
      [sam]: [['Ashfield_377405218/Ashfield_377405218', 3.1 * MB, 2000], ['Ashfield_377405218/Ashfield_377405218_old', 2.9 * MB, 3440], ['Ashfield_377405218/SaveGameInfo', 60 * KB, 2000]],
      // Added after the split: started fresh.
      [mia]: [['Clover_420551873/Clover_420551873', 1.2 * MB, 2900], ['Clover_420551873/SaveGameInfo', 41 * KB, 2900]],
    },
  }
  const splits: main.SplitView[] = ['black-myth-wukong', 'stardew-valley'].map(game => main.SplitView.createFrom({
    game, label: folders.find(f => f.id === game)?.label, accounts: [alex, sam, mia], here: alex,
  } satisfies Plain<main.SplitView>))

  const saveFiles = (list: Save[]) => list.map(([rel, size, ago]) => main.SaveFile.createFrom({ rel, size: Math.round(size), modified: iso(ago) } satisfies Plain<main.SaveFile>))
    .sort((a, b) => Date.parse(b.modified) - Date.parse(a.modified))
  const total = (list: Save[]) => Math.round(list.reduce((n, s) => n + s[1], 0))
  const newest = (list: Save[]) => list.length ? Math.min(...list.map(s => s[2])) : -1

  // The game's folder on this PC holds the active account's saves (Folders lists only that one).
  function place(game: string) {
    const f = gameFolder(game)
    const list = splitSaves[game][active] ?? []
    Object.assign(f, { id: `${game}.u-${active}`, split: true, account: active, game, bytes: total(list), files: list.length })
    if (list.length) f.modified = iso(newest(list))
    for (const s of splits) if (s.game === game) s.here = active
  }
  function gameFolder(game: string) {
    const f = folders.find(f => f.id === game || f.id.startsWith(`${game}.u-`))
    if (!f) throw new Error(`No folder ${game}`)
    return f
  }
  splits.forEach(s => place(s.game))

  const pcsOf = (id: string) => [
    ...(id === active ? [`${me} (this PC)`] : []),
    ...Object.entries(peerActive).filter(([, a]) => a === id).map(([pc]) => pc),
  ]

  const splitSave = (s: main.SplitView, acc: string) => {
    const list = splitSaves[s.game]?.[acc] ?? []
    const here = acc === active
    return main.SplitSave.createFrom({
      game: s.game, folderID: `${s.game}.u-${acc}`, label: s.label, here, synced: true,
      path: here ? gameFolder(s.game).path : `${home}\\AppData\\Local\\SyncerAccounts\\${acc}\\${s.game}`,
      files: saveFiles(list), more: 0, bytes: total(list), conflicts: 0, modified: list.length ? iso(newest(list)) : never,
    } satisfies Plain<main.SplitSave>)
  }

  const accountsView = () => main.AccountsView.createFrom({
    enabled: settings.accounts || splits.length > 0, active, waiting: [], op: '', opLabel: '', opError: '', pending: [],
    accounts: people.map(a => ({
      ...a, active: a.id === active, pcs: pcsOf(a.id),
      games: splits.filter(s => s.accounts.includes(a.id)).map(s => splitSave(s, a.id)),
    })),
    splits,
  } satisfies Plain<main.AccountsView>)

  // A shared game's saves with who last saved each (Syncthing's "modified by" and who played on that PC then).
  type Owned = [string, number, number, string]
  const sharedSaves = (f: main.FolderView): Owned[] => {
    const k = folders.indexOf(f)
    const names = ['slot1.sav', 'slot2.sav', 'slot3.sav', 'profile.sav', 'settings.ini']
    const weights = [0.32, 0.3, 0.28, 0.08, 0.02]
    const owners = [alex, sam, mia]
    return names.slice(0, Math.min(f.files, names.length))
      .map((rel, i): Owned => [rel, f.bytes * weights[i], (k * 37 + i * 410) % 3000 + 14, i < 3 ? owners[(i + k) % 3] : active])
  }

  const named = (name: string, except = '') => people.some(a => a.id !== except && a.name.toLowerCase() === name.trim().toLowerCase())
  const newAccountID = () => Array.from({ length: 6 }, () => 'abcdefghijklmnopqrstuvwxyz234567'[Math.floor(Math.random() * 32)]).join('')

  // ---- mods: Vortex manages Cyberpunk 2077 and Kingdom Come: Deliverance II here (and The Witcher 3, not synced yet) ----
  const vortex = `${home}\\AppData\\Roaming\\Vortex`
  const modFolder = (v: Plain<main.FolderView>) => main.FolderView.createFrom({
    state: 'idle', needBytes: 0, errors: 0, problem: '', backup: false, sync: true, installed: true, exists: true, shared: 2,
    conflicts: 0, backedUp: never, backupBytes: 0, points: 0, exclude: [], newerAt: null, split: false, account: '',
    modRole: '', modPhase: '', modHeld: '', modHeldBy: '', modPending: '', ...v,
  } satisfies Plain<main.FolderView>)
  const modFolders: main.FolderView[] = [
    modFolder({ id: 'cyberpunk-2077-vortex-mods', label: 'Cyberpunk 2077 (Vortex mods)', path: `${vortex}\\cyberpunk2077\\mods`, kind: 'mods', modGame: 'cyberpunk2077', bytes: 3.2 * GB, files: 4870, modified: iso(190) }),
    modFolder({ id: 'cyberpunk-2077-vortex-load-order', label: 'Cyberpunk 2077 (Vortex load order)', path: `${vortex}\\cyberpunk2077\\profiles`, kind: 'mods-profiles', modGame: 'cyberpunk2077', bytes: 214 * KB, files: 9, modified: iso(190) }),
    modFolder({ id: 'kingdom-come-deliverance-ii-vortex-mods', label: 'Kingdom Come: Deliverance II (Vortex mods)', path: 'D:\\Vortex Mods\\kingdomcomedeliverance2', kind: 'mods', modGame: 'kingdomcomedeliverance2', bytes: 4.6 * GB, files: 11240, modified: iso(1400), shared: 1 }),
    modFolder({ id: 'kingdom-come-deliverance-ii-vortex-load-order', label: 'Kingdom Come: Deliverance II (Vortex load order)', path: `${vortex}\\kingdomcomedeliverance2\\profiles`, kind: 'mods-profiles', modGame: 'kingdomcomedeliverance2', bytes: 388 * KB, files: 14, modified: iso(1400), shared: 1 }),
    // The living-room PC has no Vortex: this PC sends it Kingdom Come's deployed mods (experimental).
    modFolder({ id: 'kingdom-come-deliverance-ii-deployed-mods', label: 'Kingdom Come: Deliverance II (deployed mods)', path: 'D:\\Games\\KingdomComeDeliverance2\\Mods', kind: 'mods-deployed', modGame: 'kingdomcomedeliverance2', modRole: 'source', modPhase: 'idle', modPending: '912 files', bytes: 2.1 * GB, files: 912, modified: iso(1400), shared: 1 }),
  ]
  const modSettings = (f: main.FolderView): store.ModFolder => store.ModFolder.createFrom({
    kind: f.kind, manager: 'vortex', game: f.modGame, gameName: f.label.replace(/ \(.*\)$/, ''),
    root: f.kind === 'mods-deployed' ? `game:${f.modGame}` : `vortex:${f.modGame}`,
    rel: f.kind === 'mods-deployed' ? 'Mods' : f.kind === 'mods' ? 'staging' : 'profiles', role: f.modRole || undefined,
  } satisfies Plain<store.ModFolder>)

  // Found here but not synced yet.
  const modsFound = [
    main.GameView.createFrom({ name: 'The Witcher 3: Wild Hunt (Vortex mods)', path: `${vortex}\\witcher3\\mods`, known: true, size: 640 * MB, files: 1210, modified: iso(4300), kind: 'mods', manager: 'vortex', modGame: 'witcher3', modKey: 'vortex:witcher3/staging', syncedBy: '', installed: true, dismissed: false } satisfies Plain<main.GameView>),
    main.GameView.createFrom({ name: 'The Witcher 3: Wild Hunt (Vortex load order)', path: `${vortex}\\witcher3\\profiles`, known: true, size: 96 * KB, files: 5, modified: iso(4300), kind: 'mods-profiles', manager: 'vortex', modGame: 'witcher3', modKey: 'vortex:witcher3/profiles', syncedBy: '', installed: true, dismissed: false } satisfies Plain<main.GameView>),
  ]
  // Synced by the laptop; mod folders are added by hand here.
  const available: main.AvailableView[] = [
    main.AvailableView.createFrom({ id: 'baldur-s-gate-3-vortex-mods', label: "Baldur's Gate 3 (Vortex mods)", path: `${vortex}\\baldursgate3\\mods`, from: 'Laptop', reason: 'mods-manual', kind: 'mods', modGame: 'baldursgate3' } satisfies Plain<main.AvailableView>),
  ]

  // Vortex's mod lists, shared between the PCs that sync a game's Vortex mods.
  const shares: Record<string, Plain<main.VortexShareView>> = {
    cyberpunk2077: { game: 'cyberpunk2077', name: 'Cyberpunk 2077', mods: 46, enabled: 41, waiting: 0, checked: iso(25), applied: iso(130), appliedN: 3, err: '', peers: ['Laptop', 'Living-room PC'], pushing: false },
    kingdomcomedeliverance2: { game: 'kingdomcomedeliverance2', name: 'Kingdom Come: Deliverance II', mods: 128, enabled: 117, waiting: 0, checked: iso(40), applied: iso(1400), appliedN: 12, err: '', peers: ['Laptop'], pushing: false },
  }
  const vortexShares = () => settings.shareVortexMods
    ? Object.values(shares).filter(s => modFolders.some(f => f.kind === 'mods' && f.modGame === s.game)).map(s => main.VortexShareView.createFrom(s))
    : []

  const audits: Plain<mods.AuditEntry>[] = [
    { at: iso(1400), folder: 'kingdom-come-deliverance-ii-deployed-mods', label: 'Kingdom Come: Deliverance II (deployed mods)', phase: 'source', gen: 7, ok: true, summary: "Vortex's deployment changed: 912 files", checks: [] },
    { at: iso(60 * 24 * 6), folder: 'kingdom-come-deliverance-ii-deployed-mods', label: 'Kingdom Come: Deliverance II (deployed mods)', phase: 'source', gen: 6, ok: true, summary: "Vortex's deployment changed: 904 files", checks: [] },
    { at: iso(60 * 24 * 9), folder: 'kingdom-come-deliverance-ii-deployed-mods', label: 'Kingdom Come: Deliverance II (deployed mods)', phase: 'source', gen: 1, ok: true, summary: 'This PC sends 897 deployed mod files', checks: [] },
  ]

  let settings = store.Settings.createFrom({
    theme: params.get('theme') ?? '', backupEnabled: true, backupRoot: '', driveRoot: '', backupBackend: 'google',
    intervalHours: 6, keepDays: 30, noBackup: {}, ignored: {}, showSteamCloud: false, autoAdd: true, autoAddMaxGB: 2,
    dismissed: {}, closeToTray: true, startAtLogin: true, migrated: true, pauseWhileGaming: true, installedOnly: true,
    syncDisabled: false, backupOnly: {}, pausedUntil: null, notify: true, noUpdateCheck: false, noAutoUpdate: false,
    noCloudPull: false, noHoldWhilePlaying: false, exclude: {}, findMods: true, autoAddMods: false,
    syncDeployedMods: true, shareVortexMods: true, modsMaxGB: 5, modsChanged: iso(60 * 24 * 9),
    mods: Object.fromEntries(modFolders.map(f => [f.id, modSettings(f)])), accounts: true, launchers: {},
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
    target: `${home}\\AppData\\Local\\Syncer\\GameSaveBackup`, lastBackup, backingUp, gaming: false, paused: false,
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
    main.GameView.createFrom({ name: 'Copper Fields', path: `${home}\\Documents\\Copper Fields\\Saves`, known: true, size: 6 * MB, files: 14, modified: iso(4000), syncedBy: '', installed: true, dismissed: false } satisfies Plain<main.GameView>),
    main.GameView.createFrom({ name: 'Kestrel', path: `${home}\\AppData\\Roaming\\Kestrel`, known: true, size: 900 * KB, files: 3, modified: iso(9000), syncedBy: '', installed: true, dismissed: false } satisfies Plain<main.GameView>),
  ]

  const hoursAgo = [2, 9, 27, 50, 74, 101, 140, 190, 260, 330, 420, 520]
  const pcs = [me, 'Laptop', me, 'Living-room PC', me, 'Laptop', me, me, 'Laptop', me, 'Living-room PC', me]
  const restorePoints = (id: string) => {
    const n = all().find(f => f.id === id)?.points ?? 0
    const nowS = Math.floor(now / 1000)
    return main.RestorePointsView.createFrom({
      latest: { by: [me], from: [me] },
      points: hoursAgo.slice(0, n).map((h, k) => ({ at: nowS - h * 3600, by: [pcs[k]], from: [pcs[k]] })),
    } satisfies Plain<main.RestorePointsView>)
  }

  const conflicts = (id: string) => all().find(f => f.id === id)?.conflicts
    ? [conflict.Conflict.createFrom({
        rel: 'slot1.sav', copy: 'slot1.sync-conflict-20261003-084211-LP4XQ2M.sav', device: 'LP4XQ2M', deviceName: 'Laptop',
        size: 412 * KB, modified: iso(260), missing: false, copySize: 398 * KB, copyModified: iso(190), currentName: me,
      } satisfies Plain<conflict.Conflict>)]
    : []

  const changed = () => emit('changed')
  const all = () => [...folders, ...modFolders].sort((a, b) => a.label.toLowerCase().localeCompare(b.label.toLowerCase()))
  const folder = (id: string) => {
    const f = all().find(f => f.id === id)
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

  // Adds a mod folder found here or synced elsewhere.
  function addMod(v: Plain<main.FolderView>) {
    const f = modFolder({ ...v, id: slug(v.label ?? ''), modified: iso(1) })
    modFolders.push(f)
    settings.mods = { ...settings.mods, [f.id]: modSettings(f) }
    changed()
  }

  // Keyed and typed by the generated bindings, so a renamed Go method or changed parameters fail `npm run check`.
  const impl: { [K in keyof typeof App]?: (...a: Parameters<(typeof App)[K]>) => unknown } = {
    Overview: overview,
    Folders: all,
    Devices: () => devices,
    Available: () => available,
    ScanGames: () => found,
    ScanMods: () => modsFound,
    Accounts: accountsView,
    RestorePoints: restorePoints,
    Conflicts: conflicts,
    Decisions: () => [],
    OtherBackups: () => [] as backup.Orphan[],
    ConflictOwners: (id: string) => conflicts(id).map(c => main.ConflictOwner.createFrom({ copy: c.copy, current: active, other: sam })),
    SaveOwners: (id: string) => sharedSaves(folder(id)).sort((a, b) => a[2] - b[2])
      .map(([rel, size, ago, owner]) => main.SaveFile.createFrom({ rel, size: Math.round(size), modified: iso(ago), owner })),
    Log: () => [`${iso(14)} backup: ${folders.length} folders, 23 files updated`],
    ModSettingsDiffer: () => [],
    VortexShares: vortexShares,
    ModAudit: (id: string) => audits.filter(e => e.folder === id),
    RunModAudit: async (id: string) => {
      await wait(900)
      const f = folder(id)
      const e: Plain<mods.AuditEntry> = {
        at: new Date().toISOString(), folder: id, label: f.label, phase: 'check', gen: 7, ok: true, summary: `Checked ${f.files} mod files`,
        checks: [
          { name: 'Folder type', ok: true, detail: 'sendonly' }, { name: 'Every mod file arrived', ok: true },
          { name: 'Mod files match the source', ok: true }, { name: 'Removed mod files are gone', ok: true },
          { name: 'Plugins in the load order are installed', ok: true },
        ],
      }
      audits.unshift(e)
      return e
    },
    PushVortexList: (game: string) => {
      shares[game].pushing = true
      changed()
      setTimeout(() => { shares[game].pushing = false; shares[game].applied = new Date().toISOString(); changed() }, 4000)
    },
    AddModFolder: (key: string) => {
      const g = modsFound.find(g => g.modKey === key)
      if (!g) throw new Error('That mod folder is no longer there')
      g.syncedBy = g.name
      addMod({ label: g.name, path: g.path, kind: g.kind, modGame: g.modGame, bytes: g.size, files: g.files })
    },
    SyncAvailable: async (id: string) => {
      const a = available.find(a => a.id === id)
      if (!a) return
      await wait(500)
      available.splice(available.indexOf(a), 1)
      if (a.kind) addMod({ label: a.label, path: a.path, kind: a.kind, modGame: a.modGame, bytes: 0, files: 0, needBytes: 2.4 * GB, state: 'syncing' })
    },
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
      folders.push(main.FolderView.createFrom({ ...folders[0], id: slug(label), label, game: label, path, bytes: g?.size ?? 0, files: g?.files ?? 0, points: 0, conflicts: 0 }))
      changed()
    },

    SetAccountsEnabled: (on: boolean) => {
      if (!on && splits.length) throw new Error(`these games have separate saves per account; merge them first: ${splits.map(s => s.label).join(', ')}`)
      settings = store.Settings.createFrom({ ...settings, accounts: on })
      changed()
    },
    CreateAccount: (name: string, color: string) => {
      if (named(name)) throw new Error("there's already an account with that name")
      const a = accounts.Account.createFrom({ id: newAccountID(), name: name.trim(), color, created: new Date().toISOString(), updated: new Date().toISOString() })
      people.push(a)
      // Games already split get an empty folder for the new account: it starts fresh.
      for (const s of splits) { s.accounts.push(a.id); splitSaves[s.game][a.id] = [] }
      changed()
      return a
    },
    EditAccount: (id: string, name: string, color: string) => {
      if (named(name, id)) throw new Error("there's already an account with that name")
      Object.assign(people.find(a => a.id === id) ?? {}, { name: name.trim(), color })
      changed()
    },
    DeleteAccount: (id: string) => {
      if (splits.some(s => s.accounts.includes(id))) throw new Error('this account still has its own saves of a split game; merge those games first')
      people.splice(people.findIndex(a => a.id === id), 1)
      changed()
    },
    SwitchAccount: async (id: string) => {
      await wait(splits.length ? 900 : 200)
      active = id
      splits.forEach(s => place(s.game))
      changed()
    },
    SplitGame: async (id: string, assign: Record<string, string>) => {
      const f = folder(id)
      if (f.kind?.startsWith('mods')) throw new Error("mods are shared by every account; only a game's saves can be separated")
      if (f.split) throw new Error('this game already has separate saves per account')
      await wait(1800)
      // Every account starts with a copy of the saves as they are now.
      const saves = sharedSaves(f).map(([rel, size, ago]): Save => [rel, size, ago])
      splitSaves[id] = Object.fromEntries(people.map(a => [a.id, saves.map((s): Save => [...s])]))
      f.conflicts = Math.max(0, f.conflicts - Object.keys(assign ?? {}).length)
      splits.push(main.SplitView.createFrom({ game: id, label: f.label, accounts: people.map(a => a.id), here: active }))
      place(id)
      changed()
    },
    MergeGame: async (game: string, winner: string) => {
      const s = splits.find(s => s.game === game)
      if (!s) throw new Error("this game isn't split")
      await wait(1200)
      const f = gameFolder(game)
      const list = splitSaves[game][winner] ?? []
      Object.assign(f, { id: game, split: false, account: '', game: f.label, bytes: total(list), files: list.length })
      if (list.length) f.modified = iso(newest(list))
      splits.splice(splits.indexOf(s), 1)
      changed()
    },
  }

  const w = window as any
  // Unknown calls (opening folders, pickers, sign-in) quietly do nothing.
  w.go = { main: { App: new Proxy({}, { get: (_, name: string) => async (...args: unknown[]) => {
    const fn = impl[name as keyof typeof App] as ((...a: unknown[]) => unknown) | undefined
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
