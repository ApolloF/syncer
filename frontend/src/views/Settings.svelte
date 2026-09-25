<script lang="ts">
  import Icon from '../lib/Icon.svelte'
  import Toggle from '../lib/Toggle.svelte'
  import Modal from '../lib/Modal.svelte'
  import { ui, attempt, fail, toast, refresh, applyTheme } from '../lib/state.svelte'
  import { SaveSettings, OpenSyncthingGUI, Log, UndoAll } from '../../wailsjs/go/main/App'
  import type { store, main } from '../../wailsjs/go/models'

  const o = $derived(ui.overview)
  let log = $state<string[] | null>(null)

  async function save(patch: Partial<store.Settings>) {
    if (!o) return
    if (patch.theme) applyTheme(patch.theme)
    await attempt(() => SaveSettings({ ...o.settings, ...patch } as store.Settings))
    refresh()
  }

  const sizes = [1, 5, 10, 25, 50, 100, -1]

  const themes = [
    { id: 'system', label: 'System' },
    { id: 'light', label: 'Light' },
    { id: 'dark', label: 'Dark' },
  ]

  let undoOpen = $state(false)
  let undoBusy = $state(false)
  let undoReport = $state<main.UndoReport | null>(null)
  let opts = $state({ unpair: true, stopBackups: false, deleteBackups: false, stopSyncthing: true, uninstallSyncthing: false })

  $effect(() => { if (!opts.stopSyncthing) opts.uninstallSyncthing = false })
  $effect(() => { if (!opts.stopBackups) opts.deleteBackups = false })

  function openUndo() {
    opts = { unpair: true, stopBackups: false, deleteBackups: false, stopSyncthing: true, uninstallSyncthing: false }
    undoReport = null
    undoOpen = true
  }

  async function doUndo() {
    undoBusy = true
    try {
      const report = await UndoAll(opts as main.UndoOptions)
      toast(`${report.folders} games and ${report.devices} PCs unlinked`, 'ok')
      refresh()
      if (report.notes?.length) undoReport = report
      else undoOpen = false
    } catch (e) { fail(e) }
    undoBusy = false
  }
</script>

<header>
  <h1>Settings</h1>
</header>

{#if o?.settings.syncDisabled}
  <div class="card step">
    <div class="ic"><Icon name="alert" size={18} /></div>
    <p class="muted">Syncing is off on this PC. Sync a game or link a PC to turn it back on.</p>
  </div>
{/if}

<div class="card flush list">
  <div class="item">
    <div class="grow"><div class="name">Appearance</div></div>
    <div class="seg">
      {#each themes as t}
        <button class:active={(o?.settings.theme || 'system') === t.id} onclick={() => save({ theme: t.id })}>{t.label}</button>
      {/each}
    </div>
  </div>
  <div class="item">
    <div class="grow"><div class="name">Start with Windows</div><div class="faint small">Syncer starts in the tray when you sign in, so new games and changes from your other PCs are picked up right away.</div></div>
    <Toggle checked={o?.settings.startAtLogin} label="Start with Windows" onchange={(v) => save({ startAtLogin: v })} />
  </div>
  <div class="item">
    <div class="grow"><div class="name">Keep running in the tray</div><div class="faint small">Closing the window leaves Syncer running next to the clock. Quit from its tray menu.</div></div>
    <Toggle checked={o?.settings.closeToTray} label="Keep running in the tray" onchange={(v) => save({ closeToTray: v })} />
  </div>
  <div class="item">
    <div class="grow"><div class="name">Sync new games automatically</div><div class="faint small">Games found on this PC start syncing and backing up without a click. Games you stop syncing stay off.</div></div>
    <Toggle checked={o?.settings.autoAdd} label="Sync new games automatically" onchange={(v) => save({ autoAdd: v })} />
  </div>
  <div class="item">
    <div class="grow"><div class="name">Largest save to add automatically</div><div class="faint small">Bigger save folders stay in “Found on this PC” for you to add by hand.</div></div>
    <select disabled={!o?.settings.autoAdd} value={o?.settings.autoAddMaxGB} onchange={(e) => save({ autoAddMaxGB: +e.currentTarget.value })}>
      {#each sizes as g}<option value={g}>{g === -1 ? 'No limit' : `${g} GB`}</option>{/each}
      {#if o && !sizes.includes(o.settings.autoAddMaxGB)}<option value={o.settings.autoAddMaxGB}>{o.settings.autoAddMaxGB} GB</option>{/if}
    </select>
  </div>
  <div class="item">
    <div class="grow"><div class="name">Include Steam Cloud games</div><div class="faint small">Also sync and back up games Steam Cloud already keeps for your account. Games where Steam Cloud can't be confirmed on this PC are always included.</div></div>
    <Toggle checked={o?.settings.showSteamCloud} label="Include Steam Cloud games" onchange={(v) => save({ showSteamCloud: v })} />
  </div>
  <div class="item">
    <div class="grow"><div class="name">Only sync installed games</div><div class="faint small">Games from your other PCs are added here only when the game is installed on this PC.</div></div>
    <Toggle checked={o?.settings.installedOnly} label="Only sync installed games" onchange={(v) => save({ installedOnly: v })} />
  </div>
  <div class="item">
    <div class="grow"><div class="name">Advanced sync settings</div><div class="faint small">Syncthing's own interface, for fine-tuning.</div></div>
    <button class="btn sm" disabled={!o?.syncthing.running} onclick={() => OpenSyncthingGUI()}><Icon name="external" size={14} /> Open</button>
  </div>
  <div class="item">
    <div class="grow"><div class="name">Activity log</div></div>
    <button class="btn sm" onclick={async () => (log = log ? null : ((await Log()) ?? []))}>{log ? 'Hide' : 'Show'}</button>
  </div>
</div>

{#if log}
  <div class="card logbox selectable">
    {#each [...log].reverse() as l}<div class="mono">{l}</div>{:else}<p class="faint">Nothing yet.</p>{/each}
  </div>
{/if}

<p class="faint small about">
  Syncer keeps saves in sync with <b>Syncthing</b> (peer-to-peer, nothing goes through a server) and backs them up into
  <b>Google Drive for desktop</b>. Game locations come from the Ludusavi manifest (PCGamingWiki).
</p>

<div class="card flush list">
  <div class="item">
    <div class="grow">
      <div class="name">Undo everything</div>
      <div class="faint small">Stop all syncing on this PC and put things back the way they were before Syncer. Save files are never deleted.</div>
    </div>
    <button class="btn danger" onclick={openUndo}><Icon name="undo" size={14} /> Undo…</button>
  </div>
</div>

{#if undoOpen}
  <Modal title="Undo everything?" onclose={() => { if (!undoBusy) undoOpen = false }}>
    {#if undoReport}
      <p>{undoReport.folders} game{undoReport.folders === 1 ? '' : 's'} and {undoReport.devices} PC{undoReport.devices === 1 ? '' : 's'} unlinked.</p>
      {#if undoReport.notes.length}
        <ul class="notes">
          {#each undoReport.notes as n}<li>{n}</li>{/each}
        </ul>
      {/if}
    {:else}
      <label class="chk"><input type="checkbox" bind:checked={opts.unpair} /> Unpair all linked PCs</label>
      <label class="chk"><input type="checkbox" bind:checked={opts.stopSyncthing} /> Stop Syncthing and remove its autostart</label>
      <label class="chk"><input type="checkbox" bind:checked={opts.uninstallSyncthing} disabled={!opts.stopSyncthing} /> Uninstall Syncthing</label>
      <label class="chk"><input type="checkbox" bind:checked={opts.stopBackups} /> Stop Google Drive backups too</label>
      {#if !opts.stopBackups}<p class="faint small indent">Your games keep being backed up.</p>{/if}
      <label class="chk"><input type="checkbox" bind:checked={opts.deleteBackups} disabled={!opts.stopBackups} /> Delete Google Drive backups and history</label>
      {#if opts.deleteBackups}<p class="err small indent">This can't be undone from Syncer.</p>{/if}
    {/if}
    {#snippet actions()}
      {#if undoReport}
        <button class="btn" onclick={() => (undoOpen = false)}>Close</button>
      {:else}
        <button class="btn" disabled={undoBusy} onclick={() => (undoOpen = false)}>Cancel</button>
        <button class="btn primary" disabled={undoBusy} onclick={doUndo}>
          {#if undoBusy}<Icon name="refresh" size={15} class="spin" />{/if} Undo everything
        </button>
      {/if}
    {/snippet}
  </Modal>
{/if}

<style>
  .name { font-weight: 500; }
  .small { font-size: 12.5px; }
  .seg { display: flex; padding: 3px; gap: 2px; border-radius: 9px; background: var(--hover); }
  .seg button {
    height: 28px; padding: 0 14px; border: 0; border-radius: 7px; background: transparent;
    color: var(--muted); font: inherit; font-weight: 500; cursor: pointer; transition: background .12s, color .12s;
  }
  .seg button.active { background: var(--surface); color: var(--text); box-shadow: var(--shadow); }
  .logbox { max-height: 320px; overflow-y: auto; display: flex; flex-direction: column; gap: 2px; }
  .logbox .mono { font-size: 11.5px; color: var(--muted); white-space: pre-wrap; word-break: break-all; }
  .about { padding: 0 4px; line-height: 1.6; }
  .step { display: flex; align-items: center; gap: 14px; padding: 14px 18px; margin-bottom: 14px; }
  .ic {
    width: 34px; height: 34px; border-radius: 9px; flex: none; display: grid; place-items: center;
    background: var(--accent-soft); color: var(--accent);
  }
  .chk { display: flex; align-items: center; gap: 10px; cursor: pointer; color: var(--text); }
  .chk input { accent-color: var(--accent); }
  .chk:has(input:disabled) { opacity: .5; cursor: default; }
  .indent { margin: -6px 0 0 26px; }
  .err { color: var(--err); }
  .notes { margin: 0; padding-left: 18px; color: var(--muted); font-size: 13px; }
</style>
