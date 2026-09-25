<script module lang="ts">
  import type { main } from '../../wailsjs/go/models'
  // Survive tab switches so the list doesn't flash or rescan every time.
  const cache = $state({
    folders: [] as main.FolderView[],
    found: null as main.GameView[] | null,
    available: null as main.AvailableView[] | null,
    tab: 'synced' as 'synced' | 'found' | 'other',
  })
</script>

<script lang="ts">
  import { slide } from 'svelte/transition'
  import Icon from '../lib/Icon.svelte'
  import Toggle from '../lib/Toggle.svelte'
  import Modal from '../lib/Modal.svelte'
  import { ui, attempt, fail, refresh, toast } from '../lib/state.svelte'
  import { bytes, ago, when } from '../lib/fmt'
  import {
    Folders, ScanGames, AddFolder, AddBackupOnly, RemoveFolder, SetFolderBackup, SetFolderSync, OpenPath,
    PickFolder, RestorePoints, Restore, SaveSettings, Available, SyncAvailable, RemoveUninstalled,
    Conflicts, ResolveConflict,
  } from '../../wailsjs/go/main/App'
  import type { conflict, store } from '../../wailsjs/go/models'

  let loading = $state(false)
  let scanning = $state(false)
  let loadingAvailable = $state(false)
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

  $effect(() => { ui.tick; load(); if (cache.tab === 'other') loadAvailable() })
  $effect(() => { if (cache.tab === 'found' && cache.found === null && !scanning) scan() })
  // Games added in the background: the "found" list is out of date.
  let seenAdded = ui.gamesAdded
  $effect(() => { if (ui.gamesAdded !== seenAdded) { seenAdded = ui.gamesAdded; if (cache.found) scan() } })
  $effect(() => { if (cache.tab === 'other' && cache.available === null && !loadingAvailable) loadAvailable() })

  const found = $derived.by(() => {
    const q = query.trim().toLowerCase()
    return (cache.found ?? []).filter(g =>
      !g.syncedBy && (showCloud || !g.steamCloud) &&
      (!q || g.name.toLowerCase().includes(q) || g.path.toLowerCase().includes(q)))
  })
  const hiddenCloud = $derived((cache.found ?? []).filter(g => !g.syncedBy && g.steamCloud).length)
  const synced = $derived.by(() => {
    const q = query.trim().toLowerCase()
    return cache.folders.filter(f => !q || f.label.toLowerCase().includes(q) || f.path.toLowerCase().includes(q))
  })
  const uninstalledCount = $derived(cache.folders.filter(f => f.sync && !f.installed).length)

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
      toast(`Restored ${n} file${n === 1 ? '' : 's'} into ${restoreFor.label}`, 'ok')
      restoreFor = null
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

  async function stopUninstalled() {
    stoppingUninstalled = true
    try {
      const n = await RemoveUninstalled()
      toast(`Stopped syncing ${n} game${n === 1 ? '' : 's'}`, 'ok')
      load(); refresh()
    } catch (e) { fail(e) }
    stoppingUninstalled = false
  }

  async function syncHere(a: main.AvailableView) {
    syncingId = a.id
    if (await attempt(() => SyncAvailable(a.id), `Now syncing ${a.label}`)) {
      cache.available = cache.available?.filter(x => x.id !== a.id) ?? null
      load(); refresh()
    }
    syncingId = ''
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

<header class="row">
  <div class="grow">
    <h1>Games</h1>
    <p class="muted">Saves that sync between your PCs and back up to Google Drive.</p>
  </div>
  <button class="btn" onclick={pickCustom}><Icon name="folder" size={16} /> Add folder</button>
</header>

<div class="bar row">
  <div class="tabs">
    <button class:active={cache.tab === 'synced'} onclick={() => (cache.tab = 'synced')}>
      Synced <span class="count">{cache.folders.length}</span>
    </button>
    <button class:active={cache.tab === 'found'} onclick={() => (cache.tab = 'found')}>
      Found on this PC {#if cache.found}<span class="count">{found.length}</span>{/if}
    </button>
    <button class:active={cache.tab === 'other'} onclick={() => (cache.tab = 'other')}>
      On other PCs {#if cache.available}<span class="count">{cache.available.length}</span>{/if}
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
  {/if}
</div>

{#if cache.tab === 'synced'}
  {#if !o?.syncthing.running && cache.folders.length === 0}
    <div class="card empty"><Icon name="alert" size={22} /><p>Sync isn't running. Start it from the Overview.</p></div>
  {:else if synced.length === 0 && !loading}
    <div class="card empty">
      <Icon name="games" size={22} />
      <p>No games synced yet.</p>
      <button class="btn primary" onclick={() => (cache.tab = 'found')}>Find games on this PC</button>
    </div>
  {:else}
    {#if !o?.syncthing.running && !o?.settings.syncDisabled}
      <div class="card notice row"><Icon name="alert" size={16} /><span class="grow">Sync isn't running, so only backed-up games are listed. Start it from the Overview.</span></div>
    {/if}
    {#if o?.settings.installedOnly && uninstalledCount > 0}
      <div class="card notice row">
        <span class="grow">{uninstalledCount} synced game{uninstalledCount === 1 ? '' : 's'} aren't installed on this PC.</span>
        <button class="btn sm" disabled={stoppingUninstalled} onclick={stopUninstalled}>
          {#if stoppingUninstalled}<Icon name="refresh" size={14} class="spin" />{/if} Stop syncing them
        </button>
      </div>
    {/if}
    <div class="card flush list">
      {#each synced as f (f.id)}
        {@const s = stateOf(f)}
        <div class="item" transition:slide={{ duration: 150 }}>
          <div class="grow">
            <div class="name ellipsis">{f.label}</div>
            <div class="path faint ellipsis" title={f.path}>{f.path}</div>
          </div>
          <span class="meta faint">{bytes(f.bytes)}</span>
          {#if f.conflicts}
            <button class="pill warn linkish" title="Two PCs changed the same save — choose which to keep" onclick={() => openConflicts(f)}>
              {f.conflicts === 1 ? '2 versions' : `${f.conflicts} conflicts`}
            </button>
          {/if}
          {#if !f.installed}<span class="pill warn">Not installed</span>{/if}
          {#if f.sync}<span class="pill {s.kind}">{s.text}</span>
          {:else if f.backup}<span class="pill">Backup only</span>
          {:else}<span class="pill" title="Neither synced nor backed up. Turn either toggle back on to include it again.">Off</span>{/if}
          <div class="acts">
            <button class="btn ghost icon sm" title="Open folder" onclick={() => OpenPath(f.path)}><Icon name="folder" size={16} /></button>
            <button class="btn ghost icon sm" title="Restore from backup" onclick={() => openRestore(f)}><Icon name="history" size={16} /></button>
            <button class="btn ghost icon sm danger" title="Remove from Syncer" onclick={() => openRemove(f)}><Icon name="trash" size={16} /></button>
          </div>
          <span title="Sync between PCs"><Toggle checked={f.sync} label="Sync between PCs" onchange={(v) => toggleSync(f, v)} /></span>
          <span title="Back up to Google Drive">
            <Toggle checked={f.backup} label="Back up" onchange={(v) => toggleBackup(f, v)} />
          </span>
        </div>
      {/each}
    </div>
    <p class="faint hint">Sync = share with your other PCs. Backup = copy to Google Drive. Turn both off to keep a game listed but leave it alone.</p>
  {/if}
{:else if cache.tab === 'found'}
  {#if scanning && !cache.found}
    <div class="card empty"><Icon name="refresh" size={22} class="spin" /><p>Looking for save folders…</p></div>
  {:else}
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
        <div class="empty inner"><p class="muted">{query ? 'Nothing matches.' : 'Everything found is already synced.'}</p></div>
      {/each}
    </div>
    <div class="row hint">
      <Toggle checked={showCloud} label="Include Steam Cloud games"
        onchange={async (v) => { if (o) { await attempt(() => SaveSettings({ ...o.settings, showSteamCloud: v } as store.Settings)); refresh() } }} />
      <span class="faint">Also sync games Steam Cloud already covers{hiddenCloud && !showCloud ? ` (${hiddenCloud} hidden)` : ''}</span>
    </div>
    <p class="faint hint">{autoOn ? `New games are synced automatically; unrecognized folders${(o?.settings.autoAddMaxGB ?? 1) > 0 ? ` and saves over ${o?.settings.autoAddMaxGB ?? 1} GB` : ''} need a click.` : 'Automatic syncing of new games is off (Settings).'}</p>
  {/if}
{:else}
  {#if loadingAvailable && !cache.available}
    <div class="card empty"><Icon name="refresh" size={22} class="spin" /><p>Checking your other PCs…</p></div>
  {:else if (cache.available ?? []).length === 0}
    <div class="card empty"><Icon name="devices" size={22} /><p>Nothing else on your other PCs.</p></div>
  {:else}
    <div class="card flush list">
      {#each cache.available ?? [] as a (a.id)}
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
{/if}

{#if restoreFor}
  <Modal title="Restore {restoreFor.label}" onclose={() => (restoreFor = null)}>
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
  <Modal title="Remove {removeFor.label} from Syncer?" onclose={() => (removeFor = null)}>
    <p>Syncer stops syncing and backing up this game on this PC and removes its sync markers. Your save files stay where they are; your other PCs keep their copy.</p>
    <label class="chk"><input type="checkbox" bind:checked={deleteBackupToo} /> Also delete its Google Drive backup and history</label>
    {#if deleteBackupToo}<p class="err small">This can't be undone from Syncer.</p>{/if}
    {#snippet actions()}
      <button class="btn" onclick={() => (removeFor = null)}>Cancel</button>
      <button class="btn primary" onclick={doRemove}>Remove</button>
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
  .search { position: relative; max-width: 320px; margin-left: auto; }
  .search :global(svg) { position: absolute; left: 10px; top: 9px; color: var(--faint); }
  .search input { width: 100%; padding-left: 32px; }
  .name { font-weight: 500; }
  .name-row { gap: 8px; }
  .path { font-size: 12px; margin-top: 1px; }
  .meta { font-size: 12.5px; white-space: nowrap; }
  .acts { display: flex; gap: 2px; opacity: 0; transition: opacity .12s; }
  .item:hover .acts { opacity: 1; }
  .empty { display: flex; flex-direction: column; align-items: center; gap: 12px; padding: 40px; color: var(--muted); text-align: center; }
  .empty.inner { padding: 28px; }
  .hint { font-size: 12.5px; gap: 10px; padding: 0 4px; }
  .points { display: flex; flex-direction: column; gap: 2px; max-height: 260px; overflow-y: auto; }
  .pt { display: flex; align-items: center; gap: 10px; padding: 8px 10px; border-radius: 7px; color: var(--text); cursor: pointer; }
  .pt:hover { background: var(--hover); }
  .pt input { accent-color: var(--accent); }
  .linkish { border: 0; cursor: pointer; font: inherit; font-size: 12px; }
  .conflicts { display: flex; flex-direction: column; gap: 12px; max-height: 340px; overflow-y: auto; }
  .conf { display: flex; flex-direction: column; gap: 8px; }
  .versions { display: grid; grid-template-columns: 1fr 1fr; gap: 8px; }
  .ver { display: flex; flex-direction: column; gap: 6px; align-items: flex-start; padding: 10px; border-radius: 8px; background: var(--hover); }
  .notice { gap: 12px; padding: 12px 16px; margin-bottom: 12px; align-items: center; }
  .chk { display: flex; align-items: center; gap: 10px; cursor: pointer; color: var(--text); }
  .chk input { accent-color: var(--accent); }
  .err { color: var(--err); }
  .small { font-size: 12.5px; }
</style>
