<script lang="ts">
  import { slide } from 'svelte/transition'
  import Icon from '../lib/Icon.svelte'
  import Modal from '../lib/Modal.svelte'
  import SplitDialog from '../lib/SplitDialog.svelte'
  import { ui, attempt, fail, refresh, refreshAccounts, accountName, accountColor } from '../lib/state.svelte'
  import { bytes, ago } from '../lib/fmt'
  import {
    CreateAccount, EditAccount, DeleteAccount, SwitchAccount, MergeGame, RetryAccountChange, Folders, SaveOwners, OpenPath,
    Conflicts, ResolveConflict,
  } from '../../wailsjs/go/main/App'
  import type { main, conflict } from '../../wailsjs/go/models'

  const colors = ['#3b82f6', '#ec4899', '#10b981', '#f59e0b', '#8b5cf6', '#ef4444', '#14b8a6', '#64748b']

  const v = $derived(ui.accounts)
  const accs = $derived(v?.accounts ?? [])

  let shared = $state<main.FolderView[]>([])
  let open = $state<Record<string, boolean>>({})
  let owners = $state<Record<string, main.SaveFile[] | null>>({})

  let newName = $state('')
  let newColor = $state(colors[0])
  let editFor = $state<main.AccountView | null>(null)
  let editName = $state('')
  let editColor = $state('')
  let deleteFor = $state<main.AccountView | null>(null)
  let mergeFor = $state<main.SplitView | null>(null)
  let winner = $state('')
  let splitFor = $state<main.FolderView | null>(null)
  let busy = $state('')

  // Two versions of a save in one account's own saves (any account's).
  let conflictsFor = $state<main.SplitSave | null>(null)
  let conflicts = $state<conflict.Conflict[]>([])
  async function openConflicts(g: main.SplitSave) {
    conflictsFor = g
    try { conflicts = (await Conflicts(g.folderID)) ?? [] } catch (e) { fail(e) }
  }
  async function resolve(c: conflict.Conflict, useCopy: boolean) {
    if (!conflictsFor) return
    const g = conflictsFor
    busy = 'resolve:' + c.copy
    const ok = await attempt(() => ResolveConflict(g.folderID, c.copy, useCopy))
    busy = ''
    if (ok) {
      conflicts = conflicts.filter(x => x.copy !== c.copy)
      if (!conflicts.length) conflictsFor = null
      refresh()
    }
  }

  const opText = $derived(v?.op === 'switch' ? `Switching to ${v.opLabel}` :
    v?.op === 'split' ? `Separating the saves of ${v.opLabel}` : v?.op === 'merge' ? `Sharing ${v.opLabel} again` : '')

  $effect(() => {
    void ui.tick
    loadShared()
  })

  async function loadShared() {
    // A launcher's own data is per account already (each card lists its part).
    // Mod folders and a launcher's own data (any kind) stay shared by every account.
    try { shared = ((await Folders()) ?? []).filter(f => f.sync && !f.split && !f.kind) } catch { /* shown elsewhere */ }
  }

  async function run(key: string, fn: () => Promise<unknown>, ok?: string) {
    busy = key
    await attempt(fn, ok)
    busy = ''
    await refresh()
  }

  async function create() {
    const name = newName.trim()
    if (!name) return
    busy = 'create'
    const ok = await attempt(() => CreateAccount(name, newColor), `Added ${name}`)
    busy = ''
    await refresh()
    if (ok) {
      newName = ''
      newColor = colors[accs.length % colors.length]
    }
  }

  function openEdit(a: main.AccountView) {
    editFor = a
    editName = a.name
    editColor = a.color || colors[0]
  }

  async function saveEdit() {
    if (!editFor) return
    const a = editFor
    await run('edit', () => EditAccount(a.id, editName, editColor))
    editFor = null
  }

  async function switchTo(a: main.AccountView) {
    await run('switch:' + a.id, () => SwitchAccount(a.id), `${a.name} is playing on this PC`)
  }

  async function merge() {
    if (!mergeFor || !winner) return
    const m = mergeFor
    await run('merge', () => MergeGame(m.game, winner), `${m.label} is shared again`)
    mergeFor = null
  }

  async function toggleOwners(f: main.FolderView) {
    open[f.id] = !open[f.id]
    if (open[f.id] && owners[f.id] === undefined) {
      owners[f.id] = null
      try { owners[f.id] = (await SaveOwners(f.id)) ?? [] } catch (e) { fail(e); delete owners[f.id] }
    }
  }

  function plural(n: number, word: string) { return `${n} ${word}${n === 1 ? '' : 's'}` }
  // Games with separate saves, without the launcher data listed with them.
  const splitGames = (games: main.SplitSave[]) => games.filter(g => !g.launcher)
</script>

<header>
  <h1>Accounts</h1>
  <p class="muted">Separate saves for everyone who plays on your PCs. Games stay shared until you separate them.</p>
</header>

{#if v?.op}
  <div class="card notice row">
    <Icon name="alert" size={16} />
    <span class="grow">
      {opText} didn't finish{v.opError ? `: ${v.opError}` : ''}. Nothing was lost; it continues when you retry
      (or on its own a little later).
    </span>
    <button class="btn sm" disabled={busy === 'retry'} onclick={() => run('retry', () => RetryAccountChange(), 'Done')}>Retry</button>
  </div>
{/if}

{#each v?.pending ?? [] as p (p.game)}
  <div class="card notice row">
    <Icon name={p.error ? 'alert' : 'refresh'} size={16} />
    <span class="grow">
      {p.kind === 'merge' ? `Sharing ${p.label} again` : `Separating the saves of ${p.label}`}{p.mine ? '' : ' (decided on another PC)'}
      {p.error ? `is waiting: ${p.error}` : 'happens on this PC shortly.'}
    </span>
    <button class="btn sm" disabled={busy === 'retry'} onclick={() => run('retry', () => RetryAccountChange())}>Try now</button>
  </div>
{/each}

{#if v?.waiting?.length}
  <div class="card notice row">
    <Icon name="alert" size={16} />
    <span class="grow">Update Syncer on {v.waiting.join(', ')} (and let it connect once) before separating a game's saves.</span>
  </div>
{/if}

{#if accs.length === 0}
  <div class="card empty">
    <Icon name="users" size={22} />
    <p>Who plays on this PC? Add yourself first, then the others.</p>
  </div>
{/if}

<div class="accounts">
  {#each accs as a (a.id)}
    <div class="card acc" class:active={a.active} transition:slide={{ duration: 150 }}>
      <div class="head">
        <span class="dot" style="background:{a.color || 'var(--muted)'}"></span>
        <div class="grow">
          <div class="name">{a.name}{#if a.active}<span class="pill ok here">Playing here</span>{/if}</div>
          <div class="faint small">{a.pcs?.length ? `Playing on ${a.pcs.join(', ')}` : 'Not playing on any PC right now'}</div>
        </div>
        {#if !a.active}
          <button class="btn sm primary" disabled={!!busy} onclick={() => switchTo(a)}>
            {#if busy === 'switch:' + a.id}<Icon name="refresh" size={14} class="spin" />{:else}<Icon name="user" size={14} />{/if}
            Switch to {a.name}
          </button>
        {/if}
        <button class="btn ghost icon sm" title="Rename" onclick={() => openEdit(a)}><Icon name="edit" size={16} /></button>
        <button class="btn ghost icon sm danger" title="Remove account" onclick={() => (deleteFor = a)}><Icon name="trash" size={16} /></button>
      </div>
      {#if a.games.length}
        <div class="games">
          {#each a.games as g (g.folderID)}
            <div class="game">
              <button class="linkrow" aria-expanded={!!open[g.folderID]} onclick={() => (open[g.folderID] = !open[g.folderID])}>
                <Icon name="chevron" size={14} class={open[g.folderID] ? '' : 'rot'} />
                <span class="grow ellipsis">{g.label}</span>
                {#if !g.synced}<span class="pill warn" title="These saves haven't reached this PC yet">Not here yet</span>
                {:else}
                  <span class="faint small">{plural(g.files.length + g.more, 'file')} · {bytes(g.bytes)} · {ago(g.modified)}</span>
                  {#if g.launcher}<span class="pill" title="{g.launcher} keeps each account's playtime, achievements and settings in a file of its own, so there's nothing to separate. Sync and backup: Settings → Launchers.">Kept apart by {g.launcher}</span>
                  {:else if g.here}<span class="pill" title="In the game's save folder on this PC">In use here</span>{/if}
                {/if}
              </button>
              {#if g.conflicts}
                <button class="pill warn linkish resolve" title="Two versions of a save: choose which to keep" onclick={() => openConflicts(g)}>
                  {g.conflicts === 1 ? '2 versions' : `${g.conflicts} conflicts`} · choose
                </button>
              {/if}
              {#if open[g.folderID] && g.synced}
                <div class="files" transition:slide={{ duration: 120 }}>
                  {#each g.files as f}
                    <div class="file"><span class="mono ellipsis grow" title={f.rel}>{f.rel}</span><span class="faint">{bytes(f.size)} · {ago(f.modified)}</span></div>
                  {:else}
                    <p class="faint small">No saves yet{a.active ? '' : `: ${a.name} starts fresh in this game`}.</p>
                  {/each}
                  {#if g.more}<p class="faint small">… and {g.more} more</p>{/if}
                  <button class="btn ghost sm" onclick={() => OpenPath(g.path)}><Icon name="folder" size={14} /> Open folder</button>
                </div>
              {/if}
            </div>
          {/each}
        </div>
      {/if}
      {#if !splitGames(a.games).length}
        <p class="faint small">No separate saves yet: {a.name} uses the shared saves of every game.</p>
      {/if}
    </div>
  {/each}
</div>

<div class="card row add">
  <span class="dot" style="background:{newColor}"></span>
  <input class="grow" aria-label="Name" placeholder={accs.length ? 'Add someone…' : 'Your name'} bind:value={newName}
    onkeydown={(e) => e.key === 'Enter' && create()} maxlength="40" />
  <div class="swatches">
    {#each colors as c, i}<button class="sw" class:on={c === newColor} style="background:{c}" aria-label="Color {i + 1}" aria-pressed={c === newColor} onclick={() => (newColor = c)}></button>{/each}
  </div>
  <button class="btn sm primary" disabled={!newName.trim() || busy === 'create'} onclick={create}><Icon name="plus" size={14} /> Add</button>
</div>

{#if v?.splits?.length}
  <h2 class="section">Games with separate saves</h2>
  <div class="card flush list">
    {#each v.splits as s (s.game)}
      <div class="item">
        <div class="grow">
          <div class="name ellipsis">{s.label}</div>
          <div class="faint small">
            {s.accounts.map(accountName).filter(Boolean).join(', ')}
            {#if s.here} · {accountName(s.here)}'s saves are in use on this PC{/if}
          </div>
        </div>
        <button class="btn sm" onclick={() => { mergeFor = s; winner = s.here || s.accounts[0] }}><Icon name="merge" size={14} /> Share again…</button>
      </div>
    {/each}
  </div>
{/if}

{#if accs.length >= 2 && shared.length}
  <h2 class="section">Shared games</h2>
  <p class="faint small hint">Everyone plays on the same saves. Games that keep one save slot per person can stay shared; separate the ones where you'd overwrite each other.</p>
  <div class="card flush list">
    {#each shared as f (f.id)}
      <div class="item col">
        <div class="row full">
          <button class="linkrow grow" aria-expanded={!!open[f.id]} onclick={() => toggleOwners(f)}>
            <Icon name="chevron" size={14} class={open[f.id] ? '' : 'rot'} />
            <span class="name ellipsis">{f.label}</span>
            {#if f.conflicts}<span class="pill warn">{f.conflicts === 1 ? '2 versions' : `${f.conflicts} conflicts`}</span>{/if}
          </button>
          <button class="btn sm" onclick={() => (splitFor = f)}><Icon name="split" size={14} /> Separate saves…</button>
        </div>
        {#if open[f.id]}
          <div class="files" transition:slide={{ duration: 120 }}>
            {#if owners[f.id] === null}<p class="faint small">Looking…</p>
            {:else}
              {#each owners[f.id] ?? [] as file}
                <div class="file">
                  <span class="mono ellipsis grow" title={file.rel}>{file.rel}</span>
                  {#if file.owner}<span class="who" title="Last saved while {accountName(file.owner)} was playing"><span class="dot sm" style="background:{accountColor(file.owner)}"></span>{accountName(file.owner) || 'someone'}</span>{/if}
                  <span class="faint">{bytes(file.size)} · {ago(file.modified)}</span>
                </div>
              {:else}
                <p class="faint small">No saves.</p>
              {/each}
            {/if}
          </div>
        {/if}
      </div>
    {/each}
  </div>
{/if}

{#if editFor}
  <Modal title="Edit {editFor.name}" onclose={() => (editFor = null)}>
    <input bind:value={editName} maxlength="40" aria-label="Name" />
    <div class="swatches">
      {#each colors as c, i}<button class="sw" class:on={c === editColor} style="background:{c}" aria-label="Color {i + 1}" aria-pressed={c === editColor} onclick={() => (editColor = c)}></button>{/each}
    </div>
    {#snippet actions()}
      <button class="btn" onclick={() => (editFor = null)}>Cancel</button>
      <button class="btn primary" disabled={!editName.trim() || busy === 'edit'} onclick={saveEdit}>Save</button>
    {/snippet}
  </Modal>
{/if}

{#if deleteFor}
  {@const d = deleteFor}
  {@const games = splitGames(d.games)}
  <Modal title="Remove {d.name}?" onclose={() => (deleteFor = null)}>
    {#if games.length}
      <p>{d.name} has separate saves of {games.map(g => g.label).join(', ')}. Share those games again first (below), choosing whose saves to keep.</p>
    {:else}
      <p>{d.name} is removed on all your PCs. No saves are touched.</p>
    {/if}
    {#snippet actions()}
      <button class="btn" onclick={() => (deleteFor = null)}>Cancel</button>
      <button class="btn danger" disabled={!!games.length || busy === 'delete'}
        onclick={async () => { await run('delete', () => DeleteAccount(d.id), `Removed ${d.name}`); deleteFor = null }}>Remove</button>
    {/snippet}
  </Modal>
{/if}

{#if mergeFor}
  {@const m = mergeFor}
  <Modal title="Share {m.label} again?" onclose={() => { if (busy !== 'merge') mergeFor = null }}>
    <p>Everyone will play on the same saves again. Whose saves should that be?</p>
    <div class="choices">
      {#each m.accounts as id}
        {#if accountName(id)}
          <label class="chk"><input type="radio" bind:group={winner} value={id} /> <span class="dot sm" style="background:{accountColor(id)}"></span> {accountName(id)}'s saves</label>
        {/if}
      {/each}
    </div>
    <p class="faint small">
      Nothing is deleted: every account's saves are kept as a restore point of its own backup (Games → Elsewhere → In Google Drive,
      named “{m.label} (name)”), and their folders move to SyncerAccounts\.trash. Close the game on every PC first.
    </p>
    {#snippet actions()}
      <button class="btn" disabled={busy === 'merge'} onclick={() => (mergeFor = null)}>Cancel</button>
      <button class="btn primary" disabled={busy === 'merge' || !winner} onclick={merge}>
        {#if busy === 'merge'}<Icon name="refresh" size={15} class="spin" />{/if} Share {accountName(winner)}'s saves
      </button>
    {/snippet}
  </Modal>
{/if}

{#if conflictsFor}
  {@const g = conflictsFor}
  <Modal title="Two versions of {g.label}" onclose={() => (conflictsFor = null)}>
    <p>Two PCs changed the same save in these saves. Pick which to keep; the other goes into the backup history.</p>
    <div class="conflicts">
      {#each conflicts as c (c.copy)}
        <div class="conf">
          <div class="mono ellipsis" title={c.rel}>{c.rel}</div>
          <div class="row full">
            <span class="faint small grow">Current: {c.missing ? 'deleted' : `${ago(c.modified)} · ${bytes(c.size)}`}</span>
            <button class="btn sm" disabled={!!busy} onclick={() => resolve(c, false)}>Keep current</button>
          </div>
          <div class="row full">
            <span class="faint small grow">Other{c.deviceName ? ` (from ${c.deviceName})` : ''}: {ago(c.copyModified)} · {bytes(c.copySize)}</span>
            <button class="btn sm primary" disabled={!!busy} onclick={() => resolve(c, true)}>Use this one</button>
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

{#if splitFor}
  <SplitDialog id={splitFor.id} label={splitFor.label} onclose={() => (splitFor = null)} ondone={() => { loadShared(); refreshAccounts() }} />
{/if}

<style>
  header { display: flex; flex-direction: column; gap: 4px; }
  .small { font-size: 12.5px; }
  .accounts { display: grid; grid-template-columns: repeat(auto-fill, minmax(380px, 1fr)); gap: 14px; }
  .acc { display: flex; flex-direction: column; gap: 12px; padding: 16px 18px; }
  .acc.active { border-color: var(--accent); }
  .head { display: flex; align-items: center; gap: 10px; }
  .name { font-weight: 600; display: flex; align-items: center; gap: 8px; }
  .here { font-weight: 500; }
  .dot { width: 12px; height: 12px; border-radius: 50%; flex: none; display: inline-block; }
  .dot.sm { width: 9px; height: 9px; }
  .games { display: flex; flex-direction: column; gap: 2px; }
  .linkrow {
    display: flex; align-items: center; gap: 8px; width: 100%; padding: 7px 6px; border: 0; border-radius: 7px;
    background: transparent; color: var(--text); font: inherit; cursor: pointer; text-align: left;
  }
  .linkrow:hover { background: var(--hover); }
  .linkrow :global(.rot) { transform: rotate(-90deg); }
  .files { display: flex; flex-direction: column; gap: 3px; padding: 4px 8px 8px 28px; align-items: flex-start; width: 100%; box-sizing: border-box; }
  .file { display: flex; gap: 12px; width: 100%; font-size: 12.5px; align-items: center; }
  .who { display: inline-flex; align-items: center; gap: 5px; font-size: 12px; }
  .add { gap: 10px; padding: 10px 14px; align-items: center; }
  .add input { min-width: 0; }
  input {
    height: 32px; padding: 0 10px; border-radius: 7px; border: 1px solid var(--border);
    background: var(--surface-2); color: var(--text); font: inherit;
  }
  .swatches { display: flex; gap: 5px; }
  .sw { width: 18px; height: 18px; border-radius: 50%; border: 2px solid transparent; cursor: pointer; padding: 0; }
  .sw.on { border-color: var(--text); }
  .notice { gap: 12px; padding: 12px 16px; align-items: center; }
  .item.col { flex-direction: column; align-items: stretch; gap: 0; }
  .row.full { display: flex; align-items: center; gap: 10px; }
  .choices { display: flex; flex-direction: column; gap: 6px; }
  .chk { display: flex; align-items: center; gap: 8px; cursor: pointer; color: var(--text); }
  .chk input { accent-color: var(--accent); }
  .hint { margin: -8px 4px 0; }
  .linkish { border: 0; cursor: pointer; font: inherit; font-size: 12px; }
  .resolve { margin: 0 0 4px 28px; }
  .conflicts { display: flex; flex-direction: column; gap: 12px; max-height: 340px; overflow-y: auto; }
  .conf { display: flex; flex-direction: column; gap: 6px; padding: 10px; border-radius: 8px; background: var(--hover); }
</style>
