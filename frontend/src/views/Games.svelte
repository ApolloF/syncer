<script module lang="ts">
  import type { main, backup } from '../../wailsjs/go/models'
  // Survive tab switches so the list doesn't flash or rescan every time.
  const cache = $state({
    folders: [] as main.FolderView[],
    found: null as main.GameView[] | null,
    available: null as main.AvailableView[] | null,
    others: null as backup.Orphan[] | null,
    tab: 'games' as 'games' | 'found' | 'other',
  })
</script>

<script lang="ts">
  import { untrack } from 'svelte'
  import { slide } from 'svelte/transition'
  import Icon from '../lib/Icon.svelte'
  import Toggle from '../lib/Toggle.svelte'
  import Modal from '../lib/Modal.svelte'
  import { ui, attempt, fail, refresh, toast } from '../lib/state.svelte'
  import { bytes, ago, when, err } from '../lib/fmt'
  import {
    Folders, ScanGames, AddFolder, AddBackupOnly, AddBackupOnlyMany, RemoveFolder, SetFolderBackup, SetFolderSync, OpenPath,
    PickFolder, RestorePoints, Restore, SaveSettings, Available, SyncAvailable, RemoveUninstalled,
    Conflicts, ResolveConflict, DeleteSaves, SetExclusions, OtherBackups, AdoptBackup, DeleteOtherBackup,
  } from '../../wailsjs/go/main/App'
  import type { conflict, store } from '../../wailsjs/go/models'

  let loading = $state(false)
  let scanning = $state(false)
  let loadingAvailable = $state(false)
  let loadingOthers = $state(false)
  let othersErr = $state('')
  let query = $state('')
  let adding = $state('')
  let restoreFor = $state<main.FolderView | null>(null)
  let points = $state<number[]>([])
  let point = $state(0)
  let restoring = $state(false)
  let removeFor = $state<main.FolderView | null>(null)
  let deleteBackupToo = $state(false)
  let custom = $state<{ path: string; name: string } | null>(null)
  let conflictsFor = $state<main.FolderView | null>(null)
  let conflicts = $state<conflict.Conflict[]>([])
  let resolving = $state('')

  // Deleting a game's saves: deliberately a few steps (see openDelete).
  let deleteFor = $state<main.FolderView | null>(null)
  let deleteTyped = $state('')
  let deleteAck = $state(false)
  let deleting = $state(false)

  let excludeFor = $state<main.FolderView | null>(null)
  let excludeText = $state('')
  let excluding = $state(false)

  let bulkOpen = $state(false)
  let bulkPick = $state<Record<string, boolean>>({})
  let bulkBusy = $state(false)

  let adoptFor = $state<backup.Orphan | null>(null)
  let adoptLabel = $state('')
  let adoptPath = $state('')
  let adoptRestore = $state(true)
  let adopting = $state(false)

  let dropFor = $state<backup.Orphan | null>(null)
  let dropTyped = $state('')
  let dropping = $state(false)

  async function openConflicts(f: main.FolderView) {
    conflictsFor = f
    try { conflicts = (await Conflicts(f.id)) ?? [] } catch (e) { fail(e) }
  }

  async function resolve(c: conflict.Conflict, useCopy: boolean) {
    if (!conflictsFor) return
    const f = conflictsFor
    resolving = c.copy
    const ok = await attempt(() => ResolveConflict(f.id, c.copy, useCopy),
      useCopy ? `Using the other version of ${c.rel}` : `Kept the current ${c.rel}`)
    resolving = ''
    if (ok) {
      conflicts = conflicts.filter(x => x.copy !== c.copy)
      if (!conflicts.length) conflictsFor = null
      load(); refresh()
    }
  }
  let stoppingUninstalled = $state(false)
  let syncingId = $state('')

  const o = $derived(ui.overview)
  const showCloud = $derived(o?.settings.showSteamCloud ?? false)
  const autoOn = $derived(o?.settings.autoAdd ?? true)

  async function load() {
    loading = true
    try { cache.folders = (await Folders()) ?? [] } catch (e) { if (o?.syncthing.running) fail(e) }
    loading = false
  }

  async function scan(refreshDb = false) {
    scanning = true
    try { cache.found = (await ScanGames(refreshDb)) ?? [] } catch (e) { fail(e) }
    scanning = false
  }

  async function loadAvailable() {
    loadingAvailable = true
    try { cache.available = (await Available()) ?? [] } catch (e) { fail(e) }
    loadingAvailable = false
  }

  // Errors stay on the page: this reloads on every change, a toast each time would nag.
  async function loadOthers(refreshList = false) {
    if (loadingOthers) return
    loadingOthers = true
    try { cache.others = (await OtherBackups(refreshList)) ?? []; othersErr = '' } catch (e) { othersErr = err(e) }
    loadingOthers = false
  }

  // Reload on every backend change and tab switch; what the loaders read must
  // not re-run this (loadOthers checks loadingOthers).
  $effect(() => {
    ui.tick
    const tab = cache.tab
    untrack(() => {
      load()
      if (tab === 'other') { loadAvailable(); loadOthers() }
    })
  })
  $effect(() => { if (cache.tab === 'found' && cache.found === null && !scanning) scan() })
  // Games added in the background: the "found" list is out of date.
  let seenAdded = ui.gamesAdded
  $effect(() => { if (ui.gamesAdded !== seenAdded) { seenAdded = ui.gamesAdded; if (cache.found) scan() } })
  $effect(() => { if (cache.tab === 'other' && cache.available === null && !loadingAvailable) loadAvailable() })

  const matches = (...s: string[]) => { const q = query.trim().toLowerCase(); return !q || s.some(x => x.toLowerCase().includes(q)) }
  const found = $derived((cache.found ?? []).filter(g => !g.syncedBy && (showCloud || !g.steamCloud) && matches(g.name, g.path)))
  const hiddenCloud = $derived((cache.found ?? []).filter(g => !g.syncedBy && g.steamCloud).length)
  const games = $derived(cache.folders.filter(f => matches(f.label, f.path)))
  const available = $derived((cache.available ?? []).filter(a => matches(a.label, a.path)))
  const others = $derived((cache.others ?? []).filter(b => matches(b.label, b.path)))
  const elsewhereCount = $derived(cache.available && cache.others ? cache.available.length + cache.others.length : null)
  const uninstalledCount = $derived(cache.folders.filter(f => f.sync && !f.installed).length)
  const notInstalled = $derived((cache.found ?? []).filter(g => !g.syncedBy && !g.installed && (showCloud || !g.steamCloud)))
  const bulkCount = $derived(notInstalled.filter(g => bulkPick[g.path]).length)

  /** A time from Go; the zero time means "never". */
  const isTime = (t: any) => !!t && new Date(t).getFullYear() > 2000
  const plural = (n: number, w: string) => `${n} ${w}${n === 1 ? '' : 's'}`

  function backupLine(f: main.FolderView): string {
    const parts = [isTime(f.backedUp) ? `Backed up ${ago(f.backedUp)}` : 'Not backed up yet']
    if (f.backupBytes) parts.push(bytes(f.backupBytes))
    if (f.points) parts.push(plural(f.points, 'restore point'))
    return parts.join(' · ')
  }

  function otherLine(b: backup.Orphan): string {
    const parts: string[] = []
    if (b.host) parts.push(b.mine ? 'Backed up from this PC' : `Backed up from ${b.host}`)
    if (isTime(b.backedUp)) parts.push(ago(b.backedUp))
    else if (isTime(b.modified)) parts.push(`last save ${ago(b.modified)}`)
    if (b.points) parts.push(plural(b.points, 'restore point'))
    return parts.join(' · ') || b.id
  }

  async function add(name: string, path: string) {
    adding = path
    const ok = await attempt(() => AddFolder(name, path), `Now syncing ${name}`)
    adding = ''
    if (ok) {
      cache.found = cache.found?.map(g => g.path === path ? { ...g, syncedBy: name } as main.GameView : g) ?? null
      load(); refresh()
    }
  }

  async function addBackupOnly(name: string, path: string) {
    adding = path
    const ok = await attempt(() => AddBackupOnly(name, path), `Backing up ${name}`)
    adding = ''
    if (ok) {
      cache.found = cache.found?.map(g => g.path === path ? { ...g, syncedBy: name } as main.GameView : g) ?? null
      load(); refresh()
    }
  }

  function openBulk() {
    // Unrecognized folders are often not saves at all: the user ticks those.
    bulkPick = Object.fromEntries(notInstalled.map(g => [g.path, g.known]))
    bulkOpen = true
  }

  async function doBulk() {
    const items = notInstalled.filter(g => bulkPick[g.path]).map(g => ({ label: g.name, path: g.path }) as main.NewFolder)
    bulkBusy = true
    try {
      const r = await AddBackupOnlyMany(items)
      const added = new Set(r.added ?? [])
      if (added.size) toast(`Backing up ${plural(added.size, 'game')} without syncing`, 'ok')
      if (r.skipped?.length) toast(`${plural(r.skipped.length, 'game')} skipped: ${r.skipped[0]}${r.skipped.length > 1 ? ' …' : ''}`, 'err')
      cache.found = cache.found?.map(g => added.has(g.path) ? { ...g, syncedBy: g.name } as main.GameView : g) ?? null
      bulkOpen = false
      load(); refresh()
    } catch (e) { fail(e) }
    bulkBusy = false
  }

  async function pickCustom() {
    try {
      const p = await PickFolder()
      if (p) custom = { path: p, name: p.split('\\').pop() ?? p }
    } catch (e) { fail(e) }
  }

  async function toggleBackup(f: main.FolderView, on: boolean) {
    f.backup = on
    if (await attempt(() => SetFolderBackup(f.id, on))) { if (!f.sync) load() }
    else f.backup = !on
  }

  async function toggleSync(f: main.FolderView, on: boolean) {
    f.sync = on
    if (await attempt(() => SetFolderSync(f.id, on))) { load(); refresh() }
    else f.sync = !on
  }

  async function openRestore(f: main.FolderView) {
    restoreFor = f
    point = 0
    points = (await RestorePoints(f.id)) ?? []
  }

  async function doRestore() {
    if (!restoreFor) return
    restoring = true
    try {
      const n = await Restore(restoreFor.id, point)
      toast(`Restored ${plural(n, 'file')} into ${restoreFor.label}`, 'ok')
      restoreFor = null
      load()
    } catch (e) { fail(e) }
    restoring = false
  }

  function openRemove(f: main.FolderView) {
    removeFor = f
    deleteBackupToo = false
  }

  async function doRemove() {
    if (!removeFor) return
    const f = removeFor
    const del = deleteBackupToo
    removeFor = null
    if (await attempt(() => RemoveFolder(f.id, del), `Removed ${f.label} from Syncer`)) {
      cache.found = null
      load(); refresh()
    }
  }

  // Reached only from the Remove dialog, and it needs the game's name typed.
  function openDelete(f: main.FolderView) {
    removeFor = null
    deleteFor = f
    deleteTyped = ''
    deleteAck = false
  }

  const sameName = (typed: string, name: string) => typed.trim().toLowerCase() === name.trim().toLowerCase()
  const deleteUnbacked = $derived(!!deleteFor && (!deleteFor.backup || !o?.target))
  const canDelete = $derived(!!deleteFor && sameName(deleteTyped, deleteFor.label) && (!deleteUnbacked || deleteAck))

  async function doDelete() {
    if (!deleteFor || !canDelete) return
    const f = deleteFor
    deleting = true
    try {
      await DeleteSaves(f.id, deleteTyped, deleteAck)
      toast(`Moved the saves of ${f.label} to the Recycle Bin. It stays in your list, backed up only.`, 'ok')
      deleteFor = null
      cache.found = null
    } catch (e) { fail(e) }
    deleting = false
    load(); refresh()
  }

  const presets = [
    { label: 'Logs', pats: ['*.log'] },
    { label: 'Crash dumps', pats: ['*.dmp'] },
    { label: 'Screenshots', pats: ['Screenshots'] },
    { label: 'Shader caches', pats: ['*shadercache*'] },
  ]
  const lines = (t: string) => t.split('\n').map(s => s.trim()).filter(Boolean)
  const hasPreset = (p: { pats: string[] }) => p.pats.every(x => lines(excludeText).some(l => l.toLowerCase() === x.toLowerCase()))

  function togglePreset(p: { pats: string[] }) {
    const cur = lines(excludeText)
    const next = hasPreset(p)
      ? cur.filter(l => !p.pats.some(x => x.toLowerCase() === l.toLowerCase()))
      : [...cur, ...p.pats.filter(x => !cur.some(l => l.toLowerCase() === x.toLowerCase()))]
    excludeText = next.join('\n')
  }

  function openExclude(f: main.FolderView) {
    excludeFor = f
    excludeText = (f.exclude ?? []).join('\n')
  }

  async function doExclude() {
    if (!excludeFor) return
    const f = excludeFor
    excluding = true
    const pats = lines(excludeText)
    if (await attempt(() => SetExclusions(f.id, pats), pats.length ? `Skipping ${plural(pats.length, 'pattern')} in ${f.label}` : `Nothing skipped in ${f.label}`)) {
      excludeFor = null
      load()
    }
    excluding = false
  }

  async function stopUninstalled() {
    stoppingUninstalled = true
    try {
      const n = await RemoveUninstalled()
      toast(`Stopped syncing ${plural(n, 'game')}. They sync again once installed here.`, 'ok')
      cache.found = null
      load(); refresh()
    } catch (e) { fail(e) }
    stoppingUninstalled = false
  }

  async function showCloudGames() {
    if (o && await attempt(() => SaveSettings({ ...o.settings, showSteamCloud: true } as store.Settings))) refresh()
  }

  async function syncHere(a: main.AvailableView) {
    syncingId = a.id
    if (await attempt(() => SyncAvailable(a.id), `Now syncing ${a.label}`)) {
      cache.available = cache.available?.filter(x => x.id !== a.id) ?? null
      load(); refresh()
    }
    syncingId = ''
  }

  function openAdopt(b: backup.Orphan) {
    adoptFor = b
    adoptLabel = b.label
    adoptPath = b.path
    adoptRestore = true
  }

  async function pickAdopt() {
    try { const p = await PickFolder(); if (p) adoptPath = p } catch (e) { fail(e) }
  }

  async function doAdopt() {
    if (!adoptFor || !adoptPath) return
    const b = adoptFor
    adopting = true
    try {
      const n = await AdoptBackup(b.id, adoptLabel, adoptPath, adoptRestore)
      toast(adoptRestore ? `Added ${adoptLabel} and restored ${plural(n, 'file')}` : `Backing up ${adoptLabel} from this PC`, 'ok')
      adoptFor = null
      cache.others = cache.others?.filter(x => x.id !== b.id) ?? null
      cache.found = null
    } catch (e) { fail(e) }
    adopting = false
    load(); loadOthers()
  }

  function openDrop(b: backup.Orphan) {
    dropFor = b
    dropTyped = ''
  }

  async function doDrop() {
    if (!dropFor || !sameName(dropTyped, dropFor.label)) return
    const b = dropFor
    dropping = true
    if (await attempt(() => DeleteOtherBackup(b.id, dropTyped), `Deleted the backup of ${b.label}`)) {
      dropFor = null
      cache.others = cache.others?.filter(x => x.id !== b.id) ?? null
    }
    dropping = false
  }

  // Why a game that supports Steam Cloud isn't left to it (steam.Reason* codes).
  function cloudNote(reason: string): { text: string; tip: string } {
    const i = reason.indexOf(':')
    const code = i < 0 ? reason : reason.slice(0, i)
    const detail = i < 0 ? '' : reason.slice(i + 1)
    const covers = 'Syncer covers these saves.'
    switch (code) {
      case 'not-installed': return { text: 'Not installed through Steam', tip: `Steam didn't install this copy (for example a repack or cracked copy), so Steam Cloud doesn't keep its saves. ${covers}` }
      case 'modified': return { text: 'Modified Steam files', tip: `The game folder has ${detail}, a sign of a crack or Steam emulator, so Steam Cloud doesn't keep its saves. ${covers}` }
      case 'emulator': return { text: `Cracked copy (${detail})`, tip: `A Steam emulator (${detail}) runs this game instead of Steam, so Steam Cloud doesn't keep its saves. ${covers}` }
      case 'outside-steam': return { text: 'Played outside Steam', tip: `These saves changed after Steam last synced them: the game was played without Steam (a crack or mod launcher). ${covers}` }
      case 'mod-saves': return { text: 'Mod saves', tip: `Mod files (${detail}) here aren't kept by Steam Cloud. ${covers}` }
      case 'untracked': return { text: 'Folder not in Steam Cloud', tip: `Steam Cloud keeps other files of this game, not this folder. ${covers}` }
      case 'no-cloud-data': return { text: 'Not in your Steam Cloud', tip: `Your Steam account on this PC has no cloud saves for this game (another account owns it, or it never synced). ${covers}` }
      case 'cloud-off': return { text: 'Steam Cloud off', tip: `Steam Cloud is switched off for your account or this game. ${covers}` }
      default: return { text: 'Steam Cloud not in use', tip: `This game supports Steam Cloud, but Steam or a signed-in account wasn't found on this PC. ${covers}` }
    }
  }

  function stateOf(f: main.FolderView): { kind: string; text: string } {
    if (!f.exists) return { kind: 'warn', text: 'Waiting' }
    if (f.errors) return { kind: 'err', text: `${f.errors} error${f.errors > 1 ? 's' : ''}` }
    switch (f.state) {
      case 'idle': return f.needBytes ? { kind: 'accent', text: `${bytes(f.needBytes)} to go` } : { kind: 'ok', text: 'Synced' }
      case 'scanning': case 'scan-waiting': return { kind: '', text: 'Scanning' }
      case 'syncing': case 'sync-preparing': case 'sync-waiting': return { kind: 'accent', text: 'Syncing' }
      case 'paused': return { kind: '', text: 'Paused' }
      case 'error': return { kind: 'err', text: 'Error' }
      default: return { kind: '', text: f.state || '…' }
    }
  }
</script>

{#snippet folderRow(f: main.FolderView)}
  {@const s = stateOf(f)}
  <div class="item" transition:slide={{ duration: 150 }}>
    <div class="grow">
      <div class="name ellipsis">{f.label}</div>
      <div class="path faint ellipsis" title={f.path}>{f.path}</div>
      {#if !f.sync && f.backup}<div class="detail faint ellipsis">{backupLine(f)}</div>{/if}
    </div>
    {#if f.sync}<span class="meta faint">{bytes(f.bytes)}</span>{/if}
    {#if f.conflicts}
      <button class="pill warn linkish" title="Two PCs changed the same save — choose which to keep" onclick={() => openConflicts(f)}>
        {f.conflicts === 1 ? '2 versions' : `${f.conflicts} conflicts`}
      </button>
    {/if}
    {#if f.sync && f.newerOn && !f.needBytes}
      <span class="pill warn" title="{f.newerOn} saved this game on {new Date(f.newerAt).toLocaleString()}, but that save hasn't reached this PC yet. Turn {f.newerOn} on and let it sync before you play here.">Newer on {f.newerOn}</span>
    {/if}
    {#if !f.installed}<span class="pill warn">Not installed</span>{/if}
    {#if f.sync}<span class="pill {s.kind}">{s.text}</span>
    {:else if !f.exists}<span class="pill" title="The save folder isn't on this PC. Restore it from the backup to bring it back.">Not on this PC</span>
    {:else if f.backup}<span class="pill">Backup only</span>
    {:else}<span class="pill" title="Neither synced nor backed up. Turn either toggle back on to include it again.">Off</span>{/if}
    <div class="acts">
      <button class="btn ghost icon sm" title="Open folder" disabled={!f.exists} onclick={() => OpenPath(f.path)}><Icon name="folder" size={16} /></button>
      <button class="btn ghost icon sm" class:set={f.exclude?.length}
        title={f.exclude?.length ? `Skipped files: ${f.exclude.join(', ')}` : 'Skip files (logs, screenshots, …)'}
        onclick={() => openExclude(f)}><Icon name="filter" size={16} /></button>
      <button class="btn ghost icon sm" title="Restore from backup" onclick={() => openRestore(f)}><Icon name="history" size={16} /></button>
      <button class="btn ghost icon sm danger" title="Remove from Syncer" onclick={() => openRemove(f)}><Icon name="trash" size={16} /></button>
    </div>
    <span title="Sync between PCs"><Toggle checked={f.sync} label="Sync between PCs" onchange={(v) => toggleSync(f, v)} /></span>
    <span title="Back up to Google Drive">
      <Toggle checked={f.backup} label="Back up" onchange={(v) => toggleBackup(f, v)} />
    </span>
  </div>
{/snippet}

<header class="row">
  <div class="grow">
    <h1>Games</h1>
    <p class="muted">Sync shares a game's saves with your other PCs. Backup copies them to Google Drive.</p>
  </div>
  <button class="btn" onclick={pickCustom}><Icon name="folder" size={16} /> Add folder</button>
</header>

<div class="bar row">
  <div class="tabs">
    <button class:active={cache.tab === 'games'} onclick={() => (cache.tab = 'games')}>
      Your games <span class="count">{cache.folders.length}</span>
    </button>
    <button class:active={cache.tab === 'found'} onclick={() => (cache.tab = 'found')}>
      Found on this PC {#if cache.found}<span class="count">{found.length}</span>{/if}
    </button>
    <button class:active={cache.tab === 'other'} onclick={() => (cache.tab = 'other')}>
      Elsewhere {#if elsewhereCount !== null}<span class="count">{elsewhereCount}</span>{/if}
    </button>
  </div>
  <div class="search grow">
    <Icon name="search" size={15} />
    <input type="search" placeholder="Search" bind:value={query} />
  </div>
  {#if cache.tab === 'found'}
    <button class="btn icon" title="Rescan (and update game database)" disabled={scanning} onclick={() => scan(true)}>
      <Icon name="refresh" size={16} class={scanning ? 'spin' : ''} />
    </button>
  {:else if cache.tab === 'other'}
    <button class="btn icon" title="Check your other PCs and Google Drive again" disabled={loadingOthers || loadingAvailable}
      onclick={() => { loadAvailable(); loadOthers(true) }}>
      <Icon name="refresh" size={16} class={loadingOthers || loadingAvailable ? 'spin' : ''} />
    </button>
  {/if}
</div>

{#if cache.tab === 'games'}
  {#if !o?.syncthing.running && !o?.settings.syncDisabled}
    <div class="card notice row"><Icon name="alert" size={16} /><span class="grow">Sync isn't running. Start it from the Overview.</span></div>
  {/if}
  {#if o?.settings.installedOnly && uninstalledCount > 0}
    <div class="card notice row">
      <span class="grow">{plural(uninstalledCount, 'synced game')} {uninstalledCount === 1 ? "isn't" : "aren't"} installed on this PC.</span>
      <button class="btn sm" disabled={stoppingUninstalled} onclick={stopUninstalled}
        title="They start syncing again once the game is installed here">
        {#if stoppingUninstalled}<Icon name="refresh" size={14} class="spin" />{/if} Stop syncing them
      </button>
    </div>
  {/if}
  {#if games.length === 0 && !loading}
    <div class="card empty">
      <Icon name="games" size={22} />
      <p>{query ? 'Nothing matches.' : 'No games yet.'}</p>
      {#if !query}<button class="btn primary" onclick={() => (cache.tab = 'found')}>Find games on this PC</button>{/if}
    </div>
  {:else}
    <div class="card flush list">
      <div class="cols faint"><span>Sync</span><span>Backup</span></div>
      {#each games as f (f.id)}{@render folderRow(f)}{/each}
    </div>
  {/if}
{:else if cache.tab === 'found'}
  {#if scanning && !cache.found}
    <div class="card empty"><Icon name="refresh" size={22} class="spin" /><p>Looking for save folders…</p></div>
  {:else}
    {#if notInstalled.length}
      <div class="card notice row">
        <span class="grow">{notInstalled.length === 1 ? '1 game' : `${notInstalled.length} games`} found here {notInstalled.length === 1 ? "isn't installed. Back up its saves without syncing them?" : "aren't installed. Back up their saves without syncing them?"}</span>
        <button class="btn sm" onclick={openBulk}>Back up only…</button>
      </div>
    {/if}
    <div class="card flush list">
      {#each found as g (g.path)}
        <div class="item" transition:slide={{ duration: 150 }}>
          <div class="grow">
            <div class="row name-row">
              <span class="name ellipsis">{g.name}</span>
              {#if g.steamCloud}<span class="pill" title="Steam installed this game, Steam Cloud keeps this folder for your Steam account on this PC, and it has the latest save">Steam Cloud</span>
              {:else if g.steamCloudUnverified}{@const n = cloudNote(g.steamCloudReason)}<span class="pill warn" title={n.tip}>{n.text}</span>{/if}
              {#if g.emulator}<span class="pill warn" title="Saves a Steam emulator ({g.emulator}) keeps for a cracked copy, where Steam would keep them in Steam Cloud">{g.emulator} saves</span>{/if}
              {#if !g.known}<span class="pill warn">Unrecognized</span>{/if}
              {#if !g.installed}<span class="pill" title="Syncer didn't find this game installed on this PC">Not installed</span>{/if}
            </div>
            <div class="path faint ellipsis" title={g.path}>{g.path}</div>
          </div>
          <span class="meta faint">{bytes(g.size)} · {ago(g.modified)}</span>
          <button class="btn ghost sm" disabled={adding === g.path} onclick={() => addBackupOnly(g.name, g.path)}>
            Back up only
          </button>
          <button class="btn sm" disabled={adding === g.path} onclick={() => add(g.name, g.path)}>
            {#if adding === g.path}<Icon name="refresh" size={14} class="spin" />{:else}<Icon name="plus" size={14} />{/if}
            Sync
          </button>
        </div>
      {:else}
        <div class="empty inner"><p class="muted">{query ? 'Nothing matches.' : 'Everything found is already in your games.'}</p></div>
      {/each}
    </div>
    <p class="faint hint">
      {autoOn ? `New games are added automatically${o?.settings.installedOnly ? ' once installed' : ''}; unrecognized folders${(o?.settings.autoAddMaxGB ?? 1) > 0 ? ` and saves over ${o?.settings.autoAddMaxGB ?? 1} GB` : ''} need a click.` : 'Adding new games automatically is off.'}
      {#if hiddenCloud && !showCloud}{plural(hiddenCloud, 'Steam Cloud game')} hidden. <button class="linkbtn" onclick={showCloudGames}>Show them</button>{/if}
    </p>
  {/if}
{:else}
  <h2 class="section">On your other PCs</h2>
  {#if loadingAvailable && !cache.available}
    <p class="faint hint">Checking your other PCs…</p>
  {:else if available.length === 0}
    <p class="faint hint">{query ? 'Nothing matches.' : 'Nothing: every game your other PCs sync is here too.'}</p>
  {:else}
    <div class="card flush list">
      {#each available as a (a.id)}
        <div class="item" transition:slide={{ duration: 150 }}>
          <div class="grow">
            <div class="name ellipsis">{a.label}</div>
            <div class="path faint ellipsis" title={a.path}>{a.path}</div>
          </div>
          <span class="meta faint">from {a.from}</span>
          {#if a.reason === 'not-installed'}<span class="pill warn">Not installed</span>{:else}<span class="pill">Removed here</span>{/if}
          <button class="btn sm" disabled={syncingId === a.id} onclick={() => syncHere(a)}>
            {#if syncingId === a.id}<Icon name="refresh" size={14} class="spin" />{:else}<Icon name="plus" size={14} />{/if}
            Sync here
          </button>
        </div>
      {/each}
    </div>
  {/if}

  <h2 class="section">In Google Drive</h2>
  {#if othersErr}
    <div class="card notice row"><Icon name="alert" size={16} /><span class="grow">{othersErr}</span></div>
  {:else if !cache.others}
    <p class="faint hint">Looking…</p>
  {:else if others.length === 0}
    <p class="faint hint">{query ? 'Nothing matches.' : 'Nothing: every backup in Google Drive belongs to one of your games.'}</p>
  {:else}
    <div class="card flush list">
      {#each others as b (b.id)}
        <div class="item" transition:slide={{ duration: 150 }}>
          <div class="grow">
            <div class="name ellipsis">{b.label}</div>
            <div class="path faint ellipsis" title={b.path || b.id}>{otherLine(b)}</div>
          </div>
          <span class="meta faint">{bytes(b.bytes)}</span>
          <div class="acts">
            <button class="btn ghost icon sm danger" title="Delete this backup" onclick={() => openDrop(b)}><Icon name="trash" size={16} /></button>
          </div>
          <button class="btn sm" onclick={() => openAdopt(b)}><Icon name="plus" size={14} /> Add to Syncer</button>
        </div>
      {/each}
    </div>
    <p class="faint hint">Backups of games removed from this PC, or that your other PCs back up. Add one to back it up from here again or to restore its saves.</p>
  {/if}
{/if}

{#if restoreFor}
  <Modal title="Restore {restoreFor.label}" onclose={() => (restoreFor = null)}>
    {#if restoreFor.backup || restoreFor.points}<p class="faint small">{backupLine(restoreFor)}</p>{/if}
    <p>Close the game first. Your current files are kept as a restore point, so this can be undone.</p>
    <div class="points">
      <label class="pt"><input type="radio" bind:group={point} value={0} /> Latest backup</label>
      {#each points as p}
        <label class="pt"><input type="radio" bind:group={point} value={p} /> As it was before {when(p)}</label>
      {/each}
    </div>
    {#snippet actions()}
      <button class="btn" onclick={() => (restoreFor = null)}>Cancel</button>
      <button class="btn primary" disabled={restoring} onclick={doRestore}>
        {#if restoring}<Icon name="refresh" size={15} class="spin" />{/if} Restore
      </button>
    {/snippet}
  </Modal>
{/if}

{#if conflictsFor}
  <Modal title="Two versions of {conflictsFor.label}" onclose={() => (conflictsFor = null)}>
    <p>Two PCs changed the same save. The game loads the current one; the other was kept aside. Close the game, then pick which to keep. The version you don't pick goes into the backup history, so you can still restore it.</p>
    <div class="conflicts">
      {#each conflicts as c (c.copy)}
        <div class="conf">
          <div class="mono ellipsis" title={c.rel}>{c.rel}</div>
          <div class="versions">
            <div class="ver">
              <div class="faint small">Current{c.missing ? ' (deleted)' : ''}</div>
              <div>{c.missing ? '—' : `${when(Date.parse(c.modified) / 1000)} · ${bytes(c.size)}`}</div>
              <button class="btn sm" disabled={!!resolving} onclick={() => resolve(c, false)}>Keep current</button>
            </div>
            <div class="ver">
              <div class="faint small">Other version{c.deviceName ? ` · from ${c.deviceName}` : ''}</div>
              <div>{when(Date.parse(c.copyModified) / 1000)} · {bytes(c.copySize)}</div>
              <button class="btn sm primary" disabled={!!resolving} onclick={() => resolve(c, true)}>
                {#if resolving === c.copy}<Icon name="refresh" size={14} class="spin" />{/if} Use this one
              </button>
            </div>
          </div>
        </div>
      {:else}
        <p class="faint">No conflicts left.</p>
      {/each}
    </div>
    {#snippet actions()}
      <button class="btn" onclick={() => (conflictsFor = null)}>Close</button>
    {/snippet}
  </Modal>
{/if}

{#if removeFor}
  {@const f = removeFor}
  <Modal title="Remove {f.label} from Syncer?" onclose={() => (removeFor = null)}>
    <p>Syncer stops syncing and backing up this game on this PC and removes its sync markers. Your save files stay where they are; your other PCs keep their copy.</p>
    <label class="chk"><input type="checkbox" bind:checked={deleteBackupToo} /> Also delete its Google Drive backup and history</label>
    {#if deleteBackupToo}<p class="err small">This can't be undone from Syncer.</p>{/if}
    {#if f.exists}
      <p class="small">Want the save files on this PC gone instead? <button class="linkbtn danger" onclick={() => openDelete(f)}>Delete the save files…</button></p>
    {/if}
    {#snippet actions()}
      <button class="btn" onclick={() => (removeFor = null)}>Cancel</button>
      <button class="btn primary" onclick={doRemove}>Remove</button>
    {/snippet}
  </Modal>
{/if}

{#if deleteFor}
  {@const f = deleteFor}
  <Modal title="Delete the saves of {f.label}?" onclose={() => { if (!deleting) deleteFor = null }}>
    <p>This moves the save folder on this PC to the Recycle Bin:</p>
    <p class="mono ellipsis" title={f.path}>{f.path}</p>
    <ul class="steps">
      {#if f.sync}<li>It stops syncing here first, so your other PCs keep their copy.</li>{/if}
      {#if !deleteUnbacked}<li>It's backed up one last time. If that fails, nothing is deleted.</li>{/if}
      <li>{f.label} stays in your games as backup only{deleteUnbacked ? '' : ', so you can restore these saves later'}.</li>
    </ul>
    {#if deleteUnbacked}
      <p class="err small">{f.label} isn't backed up. Once the Recycle Bin is emptied, these saves are gone for good.</p>
      <label class="chk"><input type="checkbox" bind:checked={deleteAck} /> I understand</label>
    {/if}
    <label class="confirm">
      <span>Type <b>{f.label}</b> to confirm</span>
      <input type="text" bind:value={deleteTyped} spellcheck="false" autocomplete="off" disabled={deleting} />
    </label>
    {#snippet actions()}
      <button class="btn" disabled={deleting} onclick={() => (deleteFor = null)}>Cancel</button>
      <button class="btn danger" disabled={!canDelete || deleting} onclick={doDelete}>
        {#if deleting}<Icon name="refresh" size={15} class="spin" />{:else}<Icon name="trash" size={15} />{/if} Delete saves
      </button>
    {/snippet}
  </Modal>
{/if}

{#if excludeFor}
  <Modal title="Skip files of {excludeFor.label}" onclose={() => { if (!excluding) excludeFor = null }}>
    <p>Files matching these patterns aren't synced or backed up on this PC. One per line, like <span class="mono">*.log</span> or <span class="mono">Screenshots</span>.</p>
    <div class="presets">
      {#each presets as p}
        <button class="btn sm" class:on={hasPreset(p)} onclick={() => togglePreset(p)}>
          {#if hasPreset(p)}<Icon name="check" size={13} />{/if} {p.label}
        </button>
      {/each}
    </div>
    <textarea bind:value={excludeText} rows="6" spellcheck="false" placeholder="*.log"></textarea>
    <p class="faint small">Files already in the backup stay there. Your other PCs keep their own list.</p>
    {#snippet actions()}
      <button class="btn" disabled={excluding} onclick={() => (excludeFor = null)}>Cancel</button>
      <button class="btn primary" disabled={excluding} onclick={doExclude}>
        {#if excluding}<Icon name="refresh" size={15} class="spin" />{/if} Save
      </button>
    {/snippet}
  </Modal>
{/if}

{#if bulkOpen}
  <Modal title="Back up games that aren't installed" onclose={() => { if (!bulkBusy) bulkOpen = false }}>
    <p>Their saves are backed up to Google Drive from this PC, without syncing them to your other PCs.</p>
    <div class="picklist">
      {#each notInstalled as g (g.path)}
        <label class="chk pick" title={g.path}>
          <input type="checkbox" bind:checked={bulkPick[g.path]} />
          <span class="grow ellipsis">{g.name}</span>
          {#if !g.known}<span class="pill warn">Unrecognized</span>{/if}
          <span class="faint small">{bytes(g.size)}</span>
        </label>
      {/each}
    </div>
    <div class="row">
      <button class="btn ghost sm" onclick={() => (bulkPick = Object.fromEntries(notInstalled.map(g => [g.path, true])))}>All</button>
      <button class="btn ghost sm" onclick={() => (bulkPick = {})}>None</button>
    </div>
    {#snippet actions()}
      <button class="btn" disabled={bulkBusy} onclick={() => (bulkOpen = false)}>Cancel</button>
      <button class="btn primary" disabled={bulkBusy || !bulkCount} onclick={doBulk}>
        {#if bulkBusy}<Icon name="refresh" size={15} class="spin" />{/if} Back up {plural(bulkCount, 'game')}
      </button>
    {/snippet}
  </Modal>
{/if}

{#if adoptFor}
  <Modal title="Add {adoptFor.label} to Syncer" onclose={() => { if (!adopting) adoptFor = null }}>
    <p>It's backed up from this PC (not synced), with its backup history{adoptFor.mine ? '' : '. That history is copied first, which can take a while for big backups'}.</p>
    <input type="text" bind:value={adoptLabel} placeholder="Game name" disabled={adopting} />
    <div class="row">
      <span class="mono ellipsis grow" title={adoptPath}>{adoptPath || 'Where should its saves go?'}</span>
      <button class="btn sm" disabled={adopting} onclick={pickAdopt}>Choose…</button>
    </div>
    <label class="chk"><input type="checkbox" bind:checked={adoptRestore} disabled={adopting} /> Restore its latest saves into this folder now</label>
    {#if adoptRestore}<p class="faint small indent">Files already there are kept as a restore point first.</p>{/if}
    {#snippet actions()}
      <button class="btn" disabled={adopting} onclick={() => (adoptFor = null)}>Cancel</button>
      <button class="btn primary" disabled={adopting || !adoptPath} onclick={doAdopt}>
        {#if adopting}<Icon name="refresh" size={15} class="spin" />{/if} Add
      </button>
    {/snippet}
  </Modal>
{/if}

{#if dropFor}
  {@const b = dropFor}
  <Modal title="Delete the backup of {b.label}?" onclose={() => { if (!dropping) dropFor = null }}>
    <p>This deletes its backup{b.points ? ` and ${plural(b.points, 'restore point')}` : ''} from Google Drive. It can't be undone from Syncer.</p>
    {#if b.host && !b.mine && isTime(b.backedUp) && Date.now() - new Date(b.backedUp).getTime() < 7 * 864e5}
      <p class="err small">{b.host} backed it up {ago(b.backedUp)}, so it may still be backing this game up and will start a new backup.</p>
    {/if}
    <label class="confirm">
      <span>Type <b>{b.label}</b> to confirm</span>
      <input type="text" bind:value={dropTyped} spellcheck="false" autocomplete="off" disabled={dropping} />
    </label>
    {#snippet actions()}
      <button class="btn" disabled={dropping} onclick={() => (dropFor = null)}>Cancel</button>
      <button class="btn danger" disabled={dropping || !sameName(dropTyped, b.label)} onclick={doDrop}>
        {#if dropping}<Icon name="refresh" size={15} class="spin" />{:else}<Icon name="trash" size={15} />{/if} Delete backup
      </button>
    {/snippet}
  </Modal>
{/if}

{#if custom}
  <Modal title="Add folder" onclose={() => (custom = null)}>
    <p class="mono ellipsis" title={custom.path}>{custom.path}</p>
    <input type="text" bind:value={custom.name} placeholder="Game name" />
    {#snippet actions()}
      <button class="btn" onclick={() => (custom = null)}>Cancel</button>
      <button class="btn ghost" onclick={() => { const c = custom!; custom = null; addBackupOnly(c.name, c.path) }}>Back up only</button>
      <button class="btn primary" onclick={() => { const c = custom!; custom = null; add(c.name, c.path) }}>Sync this folder</button>
    {/snippet}
  </Modal>
{/if}

<style>
  .bar { gap: 12px; }
  .tabs { display: flex; padding: 3px; gap: 2px; border-radius: 9px; background: var(--hover); }
  .tabs button {
    height: 28px; padding: 0 12px; border: 0; border-radius: 7px; background: transparent;
    color: var(--muted); font: inherit; font-weight: 500; cursor: pointer; display: flex; align-items: center; gap: 7px;
    transition: background .12s, color .12s;
  }
  .tabs button.active { background: var(--surface); color: var(--text); box-shadow: var(--shadow); }
  .count { font-size: 12px; color: var(--faint); }
  .cols { display: flex; justify-content: flex-end; gap: 12px; padding: 8px 14px 6px; font-size: 11.5px; }
  .cols span { width: 38px; text-align: center; }
  .search { position: relative; max-width: 320px; margin-left: auto; }
  .search :global(svg) { position: absolute; left: 10px; top: 9px; color: var(--faint); }
  .search input { width: 100%; padding-left: 32px; }
  .name { font-weight: 500; }
  .name-row { gap: 8px; }
  .path { font-size: 12px; margin-top: 1px; }
  .detail { font-size: 12px; margin-top: 2px; }
  .meta { font-size: 12.5px; white-space: nowrap; }
  .acts { display: flex; gap: 2px; opacity: 0; transition: opacity .12s; }
  .item:hover .acts, .acts:focus-within { opacity: 1; }
  .acts .set { color: var(--accent); }
  .empty { display: flex; flex-direction: column; align-items: center; gap: 12px; padding: 40px; color: var(--muted); text-align: center; }
  .empty.inner { padding: 28px; }
  .hint { font-size: 12.5px; gap: 10px; padding: 0 4px; }
  .section { padding: 0 4px; }
  .section ~ .section { margin-top: 10px; }
  .points { display: flex; flex-direction: column; gap: 2px; max-height: 260px; overflow-y: auto; }
  .pt { display: flex; align-items: center; gap: 10px; padding: 8px 10px; border-radius: 7px; color: var(--text); cursor: pointer; }
  .pt:hover { background: var(--hover); }
  .pt input { accent-color: var(--accent); }
  .linkish { border: 0; cursor: pointer; font: inherit; font-size: 12px; }
  .linkbtn { border: 0; padding: 0; background: none; color: inherit; font: inherit; cursor: pointer; text-decoration: underline; }
  .linkbtn.danger { color: var(--err); }
  .conflicts { display: flex; flex-direction: column; gap: 12px; max-height: 340px; overflow-y: auto; }
  .conf { display: flex; flex-direction: column; gap: 8px; }
  .versions { display: grid; grid-template-columns: 1fr 1fr; gap: 8px; }
  .ver { display: flex; flex-direction: column; gap: 6px; align-items: flex-start; padding: 10px; border-radius: 8px; background: var(--hover); }
  .notice { gap: 12px; padding: 12px 16px; margin-bottom: 12px; align-items: center; }
  .chk { display: flex; align-items: center; gap: 10px; cursor: pointer; color: var(--text); }
  .chk input { accent-color: var(--accent); }
  .chk:has(input:disabled) { opacity: .5; cursor: default; }
  .indent { margin: -6px 0 0 26px; }
  .err { color: var(--err); }
  .small { font-size: 12.5px; }
  .steps { margin: 0; padding-left: 18px; display: flex; flex-direction: column; gap: 4px; }
  .confirm { display: flex; flex-direction: column; gap: 6px; color: var(--text); }
  .presets { display: flex; flex-wrap: wrap; gap: 6px; }
  .presets .on { border-color: var(--accent); color: var(--accent); }
  textarea {
    width: 100%; box-sizing: border-box; resize: vertical; padding: 8px 10px;
    border-radius: 7px; border: 1px solid var(--border); background: var(--surface-2); color: var(--text);
    font-family: var(--mono); font-size: 12.5px; outline: none;
  }
  textarea:focus { border-color: var(--accent); box-shadow: 0 0 0 3px var(--accent-soft); }
  .picklist { display: flex; flex-direction: column; gap: 2px; max-height: 280px; overflow-y: auto; }
  .pick { padding: 6px 8px; border-radius: 7px; gap: 10px; }
  .pick:hover { background: var(--hover); }
</style>
