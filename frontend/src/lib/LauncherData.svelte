<script lang="ts">
  import { untrack } from 'svelte'
  import Icon from './Icon.svelte'
  import Toggle from './Toggle.svelte'
  import RestoreDialog from './RestoreDialog.svelte'
  import { ui, attempt, fail, toast, refresh } from './state.svelte'
  import { backupLine, stateOf } from './folders'
  import { Folders, SetFolderSync, SetFolderBackup, RepairFolder, OpenPath } from '../../wailsjs/go/main/App'
  import type { main } from '../../wailsjs/go/models'

  // The data a launcher (Seaglass) asks Syncer to sync and back up: playtime,
  // achievements and settings. Not a game's saves, so it lives here rather
  // than under Games; the launcher keeps each account's apart by itself.
  let folders = $state<main.FolderView[]>([])
  let restoreFor = $state<main.FolderView | null>(null)
  let repairing = $state('')
  let flash = $state('')

  $effect(() => {
    void ui.tick
    untrack(load)
  })

  async function load() {
    try { folders = ((await Folders()) ?? []).filter(f => f.kind === 'launcher') } catch { /* shown elsewhere */ }
  }

  // An issue on the Overview points here (see showIssue).
  $effect(() => {
    const id = ui.focus
    if (!id || !folders.some(f => f.id === id)) return
    untrack(() => {
      ui.focus = ''
      flash = id
      requestAnimationFrame(() => document.getElementById(`launcher-${id}`)?.scrollIntoView({ block: 'center', behavior: 'smooth' }))
      setTimeout(() => { if (flash === id) flash = '' }, 2500)
    })
  })

  async function toggleSync(f: main.FolderView, on: boolean) {
    f.sync = on
    if (await attempt(() => SetFolderSync(f.id, on))) { load(); refresh() }
    else f.sync = !on
  }

  async function toggleBackup(f: main.FolderView, on: boolean) {
    f.backup = on
    if (await attempt(() => SetFolderBackup(f.id, on))) { if (!f.sync) load() }
    else f.backup = !on
  }

  async function repair(f: main.FolderView) {
    repairing = f.id
    try {
      const what = await RepairFolder(f.id)
      toast(`Repaired ${f.label}: ${what}`, 'ok')
      load(); refresh()
    } catch (e) { fail(e) }
    repairing = ''
  }

  // "Seaglass (playtime, achievements, settings)" -> "Seaglass".
  const name = (f: main.FolderView) => f.label.replace(/ \(.*\)$/, '')
</script>

{#if folders.length}
  <h2 class="section">Launchers</h2>
  <div class="card flush list">
    {#each folders as f (f.id)}
      {@const s = stateOf(f)}
      <div class="item" class:flash={flash === f.id} id="launcher-{f.id}">
        <div class="grow">
          <div class="name">{name(f)}</div>
          <div class="faint small">
            Playtime, achievements and settings. {name(f)} keeps each account's apart; Syncer syncs them between your PCs and backs them up.
          </div>
          <div class="faint small">{backupLine(f)}</div>
          {#if f.sync && f.problem}<div class="small err" title={f.problem}>{f.problem}</div>{/if}
        </div>
        {#if f.sync}<span class="pill {s.kind}" title={f.problem || undefined}>{s.text}</span>
        {:else if f.backup}<span class="pill">Backup only</span>
        {:else}<span class="pill">Off</span>{/if}
        {#if f.repairable}
          <button class="btn sm primary" disabled={repairing === f.id} onclick={() => repair(f)}
            title="Brings back from the backup what's missing here (next to any file the launcher already started again, never over it), then syncs the folder again like a new PC: nothing is deleted on your other PCs. Syncer also does this by itself at its next background run.">
            {#if repairing === f.id}<Icon name="refresh" size={14} class="spin" />{/if} Repair now
          </button>
        {/if}
        <button class="btn ghost icon sm" title="Restore from backup" onclick={() => (restoreFor = f)}><Icon name="history" size={16} /></button>
        <button class="btn ghost icon sm" title="Open folder" disabled={!f.exists} onclick={() => OpenPath(f.path)}><Icon name="folder" size={16} /></button>
        <span title="Sync between PCs"><Toggle checked={f.sync} label="Sync between PCs" onchange={(v) => toggleSync(f, v)} /></span>
        <span title="Back up to Google Drive"><Toggle checked={f.backup} label="Back up" onchange={(v) => toggleBackup(f, v)} /></span>
      </div>
    {/each}
  </div>
{/if}

{#if restoreFor}
  <RestoreDialog folder={restoreFor} onclose={() => (restoreFor = null)} ondone={load} />
{/if}

<style>
  .section { font-size: 13px; font-weight: 600; color: var(--muted); margin: 22px 4px 8px; text-transform: uppercase; letter-spacing: .04em; }
  .name { font-weight: 500; }
  .small { font-size: 12.5px; }
  .err { color: var(--err); }
  .item.flash { background: var(--hover); box-shadow: inset 3px 0 0 var(--accent); }
</style>
