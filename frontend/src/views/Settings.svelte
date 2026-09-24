<script lang="ts">
  import Icon from '../lib/Icon.svelte'
  import Toggle from '../lib/Toggle.svelte'
  import { ui, attempt, refresh, applyTheme } from '../lib/state.svelte'
  import { SaveSettings, OpenSyncthingGUI, Log } from '../../wailsjs/go/main/App'
  import type { store } from '../../wailsjs/go/models'

  const o = $derived(ui.overview)
  let log = $state<string[] | null>(null)

  async function save(patch: Partial<store.Settings>) {
    if (!o) return
    if (patch.theme) applyTheme(patch.theme)
    await attempt(() => SaveSettings({ ...o.settings, ...patch } as store.Settings))
    refresh()
  }

  const themes = [
    { id: 'system', label: 'System' },
    { id: 'light', label: 'Light' },
    { id: 'dark', label: 'Dark' },
  ]
</script>

<header>
  <h1>Settings</h1>
</header>

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
    <div class="grow"><div class="name">Show Steam Cloud games</div><div class="faint small">Steam already syncs these; hidden from “Found on this PC” by default.</div></div>
    <Toggle checked={o?.settings.showSteamCloud} label="Show Steam Cloud games" onchange={(v) => save({ showSteamCloud: v })} />
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
</style>
