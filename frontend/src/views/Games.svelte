<script module lang="ts">
  import type { main } from '../../wailsjs/go/models'
  // Survive tab switches so the list doesn't flash or rescan every time.
  const cache = $state({ folders: [] as main.FolderView[], found: null as main.GameView[] | null, tab: 'synced' as 'synced' | 'found' })
</script>

<script lang="ts">
  import { slide } from 'svelte/transition'
  import Icon from '../lib/Icon.svelte'
  import Toggle from '../lib/Toggle.svelte'
  import Modal from '../lib/Modal.svelte'
  import { ui, attempt, fail, refresh, toast } from '../lib/state.svelte'
  import { bytes, ago, when } from '../lib/fmt'
  import {
    Folders, ScanGames, AddFolder, RemoveFolder, SetFolderBackup, OpenPath,
    PickFolder, RestorePoints, Restore, SaveSettings,
  } from '../../wailsjs/go/main/App'

  let loading = $state(false)
  let scanning = $state(false)
  let query = $state('')
  let adding = $state('')
  let restoreFor = $state<main.FolderView | null>(null)
  let points = $state<number[]>([])
  let point = $state(0)
  let restoring = $state(false)
  let removeFor = $state<main.FolderView | null>(null)
  let custom = $state<{ path: string; name: string } | null>(null)

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

  $effect(() => { ui.tick; load() })
  $effect(() => { if (cache.tab === 'found' && cache.found === null && !scanning) scan() })
  // Games added in the background: the "found" list is out of date.
  let seenAdded = ui.gamesAdded
  $effect(() => { if (ui.gamesAdded !== seenAdded) { seenAdded = ui.gamesAdded; if (cache.found) scan() } })

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

  async function add(name: string, path: string) {
    adding = path
    const ok = await attempt(() => AddFolder(name, path), `Now syncing ${name}`)
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
    if (!(await attempt(() => SetFolderBackup(f.id, on)))) f.backup = !on
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

  async function doRemove() {
    if (!removeFor) return
    const f = removeFor
    removeFor = null
    if (await attempt(() => RemoveFolder(f.id), `Stopped syncing ${f.label}`)) {
      cache.found = null
      load(); refresh()
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
  {#if !o?.syncthing.running}
    <div class="card empty"><Icon name="alert" size={22} /><p>Sync isn't running. Start it from the Overview.</p></div>
  {:else if synced.length === 0 && !loading}
    <div class="card empty">
      <Icon name="games" size={22} />
      <p>No games synced yet.</p>
      <button class="btn primary" onclick={() => (cache.tab = 'found')}>Find games on this PC</button>
    </div>
  {:else}
    <div class="card flush list">
      {#each synced as f (f.id)}
        {@const s = stateOf(f)}
        <div class="item" transition:slide={{ duration: 150 }}>
          <div class="grow">
            <div class="name ellipsis">{f.label}</div>
            <div class="path faint ellipsis" title={f.path}>{f.path}</div>
          </div>
          <span class="meta faint">{bytes(f.bytes)}</span>
          <span class="pill {s.kind}">{s.text}</span>
          <div class="acts">
            <button class="btn ghost icon sm" title="Open folder" onclick={() => OpenPath(f.path)}><Icon name="folder" size={16} /></button>
            <button class="btn ghost icon sm" title="Restore from backup" onclick={() => openRestore(f)}><Icon name="history" size={16} /></button>
            <button class="btn ghost icon sm danger" title="Stop syncing" onclick={() => (removeFor = f)}><Icon name="trash" size={16} /></button>
          </div>
          <span title="Back up to Google Drive"><Toggle checked={f.backup} label="Back up" onchange={(v) => toggleBackup(f, v)} /></span>
        </div>
      {/each}
    </div>
    <p class="faint hint">Toggle = include in Google Drive backup.</p>
  {/if}
{:else}
  {#if scanning && !cache.found}
    <div class="card empty"><Icon name="refresh" size={22} class="spin" /><p>Looking for save folders…</p></div>
  {:else}
    <div class="card flush list">
      {#each found as g (g.path)}
        <div class="item" transition:slide={{ duration: 150 }}>
          <div class="grow">
            <div class="row name-row">
              <span class="name ellipsis">{g.name}</span>
              {#if g.steamCloud}<span class="pill" title="Steam Cloud keeps this save for your Steam account on this PC">Steam Cloud</span>
              {:else if g.steamCloudUnverified}<span class="pill warn" title="This game supports Steam Cloud, but it isn't in use for your Steam account on this PC (installed outside Steam, another account, or cloud off), so Syncer covers it">Steam Cloud not in use</span>{/if}
              {#if !g.known}<span class="pill warn">Unrecognized</span>{/if}
            </div>
            <div class="path faint ellipsis" title={g.path}>{g.path}</div>
          </div>
          <span class="meta faint">{bytes(g.size)} · {ago(g.modified)}</span>
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
        onchange={async (v) => { if (o) { await attempt(() => SaveSettings({ ...o.settings, showSteamCloud: v })); refresh() } }} />
      <span class="faint">Also sync games Steam Cloud already covers{hiddenCloud && !showCloud ? ` (${hiddenCloud} hidden)` : ''}</span>
    </div>
    <p class="faint hint">{autoOn ? 'New games are synced automatically; unrecognized folders need a click.' : 'Automatic syncing of new games is off (Settings).'}</p>
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

{#if removeFor}
  <Modal title="Stop syncing {removeFor.label}?" onclose={() => (removeFor = null)}>
    <p>Nothing is deleted. The files stay on this PC and your other PCs keep their copy, but changes stop flowing.</p>
    {#snippet actions()}
      <button class="btn" onclick={() => (removeFor = null)}>Cancel</button>
      <button class="btn primary" onclick={doRemove}>Stop syncing</button>
    {/snippet}
  </Modal>
{/if}

{#if custom}
  <Modal title="Add folder" onclose={() => (custom = null)}>
    <p class="mono ellipsis" title={custom.path}>{custom.path}</p>
    <input type="text" bind:value={custom.name} placeholder="Game name" />
    {#snippet actions()}
      <button class="btn" onclick={() => (custom = null)}>Cancel</button>
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
</style>
