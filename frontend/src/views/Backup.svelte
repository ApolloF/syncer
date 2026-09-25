<script module lang="ts">
  type Prog = { folder: string; folderIdx: number; folders: number; filesDone: number; copied: number; bytes: number }
  const live = $state({ prog: null as Prog | null })
</script>

<script lang="ts">
  import { onMount } from 'svelte'
  import Icon from '../lib/Icon.svelte'
  import Toggle from '../lib/Toggle.svelte'
  import { ui, attempt, refresh, toast } from '../lib/state.svelte'
  import { ago, bytes } from '../lib/fmt'
  import { BackupNow, CancelBackup, OpenBackupFolder, PickBackupFolder, SaveSettings } from '../../wailsjs/go/main/App'
  import { EventsOn, BrowserOpenURL } from '../../wailsjs/runtime/runtime'
  import type { store } from '../../wailsjs/go/models'

  const o = $derived(ui.overview)
  const lb = $derived(o?.lastBackup)
  let showErrors = $state(false)

  onMount(() => {
    const off1 = EventsOn('backup:progress', (p: Prog) => { live.prog = p; if (ui.overview) ui.overview.backingUp = true })
    const off2 = EventsOn('backup:done', (r: any) => {
      live.prog = null
      if (r?.error) toast(r.error, 'err')
      else if (r) toast(r.ok ? `Backup done · ${r.copied} file${r.copied === 1 ? '' : 's'} updated` : `Backup finished with ${r.errors?.length} issue(s)`, r.ok ? 'ok' : 'err')
      refresh()
    })
    return () => { off1(); off2() }
  })

  async function save(patch: Partial<store.Settings>) {
    if (!o) return
    await attempt(() => SaveSettings({ ...o.settings, ...patch } as store.Settings))
    refresh()
  }

  async function start() {
    if (await attempt(BackupNow)) {
      if (ui.overview) ui.overview.backingUp = true
      live.prog = { folder: 'Starting…', folderIdx: 0, folders: 0, filesDone: 0, copied: 0, bytes: 0 }
    }
  }

  const pct = $derived(live.prog && live.prog.folders ? (live.prog.folderIdx / live.prog.folders) * 100 : 0)
  const driveLabel = $derived.by(() => {
    if (!o) return ''
    if (o.settings.backupRoot) return 'Custom folder'
    if (!o.drive.found) return o.settings.driveRoot ? 'Chosen drive not found' : 'Not found'
    return o.drive.running ? 'Connected' : 'Installed, not running'
  })
</script>

<header class="row">
  <div class="grow">
    <h1>Backup</h1>
    <p class="muted">A copy of every save in your Google Drive, with 30 days of history.</p>
  </div>
  {#if o?.backingUp}
    <button class="btn" onclick={() => CancelBackup()}><Icon name="stop" size={14} /> Stop</button>
  {:else}
    <button class="btn primary" disabled={!o?.target} onclick={start}><Icon name="upload" size={16} /> Back up now</button>
  {/if}
</header>

{#if o?.backingUp}
  <div class="card run">
    <div class="row">
      <Icon name="refresh" size={16} class="spin" />
      <span class="grow ellipsis">{live.prog?.folder ?? 'Backing up…'}</span>
      {#if live.prog?.folders}<span class="faint">{live.prog.folderIdx} / {live.prog.folders}</span>{/if}
    </div>
    <div class="progress" class:indeterminate={!pct}><div style="width:{pct}%"></div></div>
    {#if live.prog}<p class="faint small">{live.prog.copied} files updated · {bytes(live.prog.bytes)}</p>{/if}
  </div>
{/if}

<section class="grid">
  <div class="card kv">
    <h2>Google Drive</h2>
    <div class="line"><span class="muted">Status</span>
      <span class="pill {o?.settings.backupRoot || o?.drive.running ? 'ok' : o?.drive.found ? 'warn' : 'err'}">{driveLabel}</span></div>
    {#if !o?.settings.backupRoot && ((o?.drive.drives?.length ?? 0) > 1 || o?.settings.driveRoot)}
      <div class="line"><span class="muted">Account</span>
        <select value={o?.settings.driveRoot ?? ''} onchange={(e) => save({ driveRoot: e.currentTarget.value })}>
          <option value="">Automatic ({o?.drive.drives?.[0]?.myDrive ?? 'none'})</option>
          {#each o?.drive.drives ?? [] as d}
            <option value={d.myDrive}>{d.myDrive}{d.label ? ` · ${d.label}` : ''}</option>
          {/each}
          {#if o?.settings.driveRoot && !o.drive.drives?.some(d => d.myDrive.toLowerCase() === o.settings.driveRoot.toLowerCase())}
            <option value={o.settings.driveRoot}>{o.settings.driveRoot} (not found)</option>
          {/if}
        </select></div>
    {/if}
    <div class="line"><span class="muted">Folder</span>
      <span class="ellipsis mono path" title={o?.target}>{o?.target || '—'}</span></div>
    <div class="row btns">
      {#if !o?.drive.found && !o?.settings.backupRoot}
        <button class="btn primary sm" onclick={() => BrowserOpenURL('https://www.google.com/drive/download/')}><Icon name="external" size={14} /> Get Google Drive</button>
      {/if}
      <button class="btn sm" disabled={!o?.target} onclick={() => OpenBackupFolder()}><Icon name="folder" size={14} /> Open</button>
      <button class="btn ghost sm" onclick={async () => { await attempt(PickBackupFolder); refresh() }}>Change…</button>
      {#if o?.settings.backupRoot}
        <button class="btn ghost sm" onclick={() => save({ backupRoot: '' })}>Use Google Drive</button>
      {/if}
    </div>
  </div>

  <div class="card kv">
    <h2>Last backup</h2>
    <div class="line"><span class="muted">When</span><span>{ago(lb?.finished)}</span></div>
    <div class="line"><span class="muted">Result</span>
      {#if !lb}<span class="faint">—</span>
      {:else if lb.ok && !lb.folders}<span class="pill warn">No folders to back up</span>
      {:else if lb.ok}<span class="pill ok">OK · {lb.copied} updated</span>
      {:else}<button class="pill err linkish" onclick={() => (showErrors = !showErrors)}>{lb.errors?.length} issue{lb.errors?.length === 1 ? '' : 's'}</button>{/if}
    </div>
    <div class="line"><span class="muted">Uploaded</span><span>{bytes(lb?.bytes ?? 0)}</span></div>
  </div>
</section>

{#if showErrors && lb?.errors?.length}
  <div class="card errors">
    {#each lb.errors.slice(0, 50) as e}<p class="mono small selectable">{e}</p>{/each}
    <p class="faint small">Locked files (a game still running) are retried on the next run.</p>
  </div>
{/if}

<div class="card flush list">
  <div class="item">
    <div class="grow"><div class="name">Automatic backup</div><div class="faint small">Runs in the background, even when Syncer is closed.</div></div>
    <Toggle checked={o?.settings.backupEnabled} label="Automatic backup" onchange={(v) => save({ backupEnabled: v })} />
  </div>
  <div class="item">
    <div class="grow"><div class="name">Every</div></div>
    <select value={o?.settings.intervalHours} onchange={(e) => save({ intervalHours: +e.currentTarget.value })}>
      {#each [1, 3, 6, 12, 24] as h}<option value={h}>{h === 24 ? 'day' : `${h} hour${h > 1 ? 's' : ''}`}</option>{/each}
    </select>
  </div>
  <div class="item">
    <div class="grow"><div class="name">Keep old versions for</div><div class="faint small">Changed or deleted saves stay restorable this long.</div></div>
    <select value={o?.settings.keepDays} onchange={(e) => save({ keepDays: +e.currentTarget.value })}>
      {#each [7, 14, 30, 90, 365] as d}<option value={d}>{d} days</option>{/each}
    </select>
  </div>
</div>
<p class="faint small hint">Pick which games are backed up on the Games page. Restore a save from its <Icon name="history" size={13} /> button there.</p>

<style>
  .grid { display: grid; grid-template-columns: 1fr 1fr; gap: 14px; }
  .kv { display: flex; flex-direction: column; gap: 10px; }
  .line { display: flex; align-items: center; justify-content: space-between; gap: 16px; min-height: 24px; }
  .path { font-size: 12px; max-width: 260px; }
  .btns { gap: 6px; margin-top: 4px; flex-wrap: wrap; }
  .run { display: flex; flex-direction: column; gap: 10px; border-color: var(--accent); }
  .small { font-size: 12.5px; }
  .name { font-weight: 500; }
  .errors { display: flex; flex-direction: column; gap: 4px; max-height: 240px; overflow-y: auto; }
  .linkish { border: 0; cursor: pointer; font: inherit; font-size: 12px; }
  .hint { padding: 0 4px; display: flex; align-items: center; gap: 4px; }
  select { min-width: 120px; }
  @media (max-width: 860px) { .grid { grid-template-columns: 1fr; } }
</style>
