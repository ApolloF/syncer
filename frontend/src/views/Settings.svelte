<script lang="ts">
  import Icon from '../lib/Icon.svelte'
  import Toggle from '../lib/Toggle.svelte'
  import Modal from '../lib/Modal.svelte'
  import { ui, attempt, fail, toast, refresh, applyTheme } from '../lib/state.svelte'
  import { SaveSettings, OpenSyncthingGUI, Log, UndoAll, Pause, Resume, CheckForUpdate, OpenUpdate } from '../../wailsjs/go/main/App'
  import type { store, main } from '../../wailsjs/go/models'
  import { pausedUntil, tomorrowMorning } from '../lib/fmt'

  const o = $derived(ui.overview)
  let log = $state<string[] | null>(null)

  const pauses = [
    { label: '1 hour', until: () => Date.now() + 3600e3 },
    { label: '4 hours', until: () => Date.now() + 4 * 3600e3 },
    { label: 'Until tomorrow', until: () => tomorrowMorning().getTime() },
  ]

  async function pause(until: number) {
    await attempt(() => Pause(Math.round(until / 1000)), 'Syncing and backups paused')
    refresh()
  }

  async function resume() {
    await attempt(() => Resume(), 'Syncing and backups resumed')
    refresh()
  }

  let checking = $state(false)
  async function checkUpdate() {
    checking = true
    try {
      const u = await CheckForUpdate()
      toast(u ? `Syncer ${u.latest} is available` : 'You have the newest version', 'ok')
      refresh()
    } catch (e) { fail(e) }
    checking = false
  }

  async function save(patch: Partial<store.Settings>) {
    if (!o) return
    if (patch.theme) applyTheme(patch.theme)
    await attempt(() => SaveSettings({ ...o.settings, ...patch } as store.Settings))
    refresh()
  }

  const sizes = [1, 5, 10, 25, 50, 100, -1]
  const modSizes = [5, 20, 50, 100, -1]
  let deployedOpen = $state(false)

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
    <div class="grow">
      <div class="name">Pause syncing and backups</div>
      <div class="faint small">
        {#if o?.paused}Paused until {pausedUntil(o.settings.pausedUntil)}. It picks up again on its own; “Back up now” still works.
        {:else}Stop syncing and automatic backups on this PC for a while, e.g. while you play offline. It resumes on its own.{/if}
      </div>
    </div>
    {#if o?.paused}
      <button class="btn sm" onclick={resume}><Icon name="play" size={14} /> Resume now</button>
    {:else}
      <div class="seg">
        {#each pauses as p}<button onclick={() => pause(p.until())}>{p.label}</button>{/each}
      </div>
    {/if}
  </div>
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
    <div class="grow"><div class="name">Only sync installed games</div><div class="faint small">Games from your other PCs, and new games found here, start syncing only once the game is installed on this PC.</div></div>
    <Toggle checked={o?.settings.installedOnly} label="Only sync installed games" onchange={(v) => save({ installedOnly: v })} />
  </div>
  <div class="item">
    <div class="grow"><div class="name">Notify me about problems</div><div class="faint small">A Windows notification when a backup fails, no backup has worked for 3 days, a save has two versions, or a new Syncer is out.</div></div>
    <Toggle checked={o?.settings.notify} label="Notify me about problems" onchange={(v) => save({ notify: v })} />
  </div>
  <div class="item">
    <div class="grow">
      <div class="name">Check for updates</div>
      <div class="faint small">
        {#if o?.update}Syncer {o.update.latest} is available (you have {o.version}).
        {:else}Look for new Syncer releases on GitHub once a day. This is {o?.version === 'dev' ? 'a development build' : `version ${o?.version ?? ''}`}.{/if}
      </div>
    </div>
    {#if o?.update}
      <button class="btn sm primary" onclick={() => OpenUpdate()}><Icon name="external" size={14} /> Download</button>
    {:else}
      <button class="btn sm" disabled={checking || o?.settings.noUpdateCheck || o?.version === 'dev'} onclick={checkUpdate}>
        {#if checking}<Icon name="refresh" size={14} class="spin" />{/if} Check now
      </button>
    {/if}
    <Toggle checked={!o?.settings.noUpdateCheck} label="Check for updates" onchange={(v) => save({ noUpdateCheck: !v })} />
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

<h2 class="section">Mods</h2>
<div class="card flush list">
  <div class="item">
    <div class="grow">
      <div class="name">Find installed mods</div>
      <div class="faint small">
        List the mods Vortex installed for your games, and their load orders, under “Found on this PC” so you can sync them to your other PCs.
        Mod folders sync only; they aren't backed up to Google Drive unless you turn their backup on.
        They pause while Vortex is open. Open Vortex on the other PC to enable and deploy the mods that arrive.
      </div>
    </div>
    <Toggle checked={o?.settings.findMods} label="Find installed mods" onchange={(v) => save({ findMods: v })} />
  </div>
  <div class="item" class:off={!o?.settings.findMods}>
    <div class="grow">
      <div class="name">Sync new mod folders automatically <span class="pill warn">Experimental</span></div>
      <div class="faint small">Vortex's mod and load-order folders start syncing without a click, including ones your other PCs sync. Otherwise you add each one by hand.</div>
    </div>
    <select disabled={!o?.settings.findMods || !o?.settings.autoAddMods} value={o?.settings.modsMaxGB} title="Largest mod folder to add automatically"
      onchange={(e) => save({ modsMaxGB: +e.currentTarget.value })}>
      {#each modSizes as g}<option value={g}>{g === -1 ? 'No limit' : `Up to ${g} GB`}</option>{/each}
      {#if o && !modSizes.includes(o.settings.modsMaxGB)}<option value={o.settings.modsMaxGB}>Up to {o.settings.modsMaxGB} GB</option>{/if}
    </select>
    <Toggle checked={o?.settings.autoAddMods} disabled={!o?.settings.findMods} label="Sync new mod folders automatically"
      onchange={(v) => save({ autoAddMods: v })} />
  </div>
  <div class="item" class:off={!o?.settings.findMods}>
    <div class="grow">
      <div class="name">Sync deployed mods in the game folder <span class="pill warn">Experimental</span></div>
      <div class="faint small">Send the mods one PC deployed straight into the game's folder, so other PCs can play with them without Vortex. Only the mod files are synced. Each update is checked, and this PC saves a copy of what it replaces before it applies anything.</div>
    </div>
    <Toggle checked={o?.settings.syncDeployedMods} disabled={!o?.settings.findMods} label="Sync deployed mods in the game folder"
      onchange={(v) => { if (v) deployedOpen = true; else save({ syncDeployedMods: false }) }} />
  </div>
</div>

{#if deployedOpen}
  <Modal title="Sync deployed mods?" onclose={() => (deployedOpen = false)}>
    <p>This is experimental. Deployed mods go straight into your game's folder:</p>
    <ul class="notes">
      <li>Every PC needs the same version of the game. Syncer checks the version and won't apply mods when it differs.</li>
      <li>One PC is the source; the others receive. On a receiving PC, don't let Vortex deploy that game, or the two will fight over the same files.</li>
      <li>Nothing changes on a receiving PC until you apply an update. Before it applies anything, Syncer saves a copy of the files it will replace, so you can roll back.</li>
      <li>Only the files Vortex deployed are synced. The game's own files are never touched.</li>
      <li>Vortex must deploy with hardlinks (its default), not symlinks.</li>
    </ul>
    {#snippet actions()}
      <button class="btn" onclick={() => (deployedOpen = false)}>Cancel</button>
      <button class="btn primary" onclick={() => { deployedOpen = false; save({ syncDeployedMods: true }) }}>Turn on</button>
    {/snippet}
  </Modal>
{/if}

{#if log}
  <div class="card logbox selectable">
    {#each [...log].reverse() as l}<div class="mono">{l}</div>{:else}<p class="faint">Nothing yet.</p>{/each}
  </div>
{/if}

<p class="faint small about">
  Syncer keeps saves in sync with <b>Syncthing</b> (peer-to-peer, nothing goes through a server) and backs them up into
  <b>Google Drive for desktop</b>. Game locations come from the Ludusavi manifest (PCGamingWiki).
  {#if o?.version}Syncer {o.version}.{/if}
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
  .notes li + li { margin-top: 6px; }
  .section { font-size: 13px; font-weight: 600; color: var(--muted); margin: 22px 4px 8px; text-transform: uppercase; letter-spacing: .04em; }
  .off { opacity: .55; }
  .name .pill { margin-left: 6px; vertical-align: 1px; }
</style>
