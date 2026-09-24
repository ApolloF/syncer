<script lang="ts">
  import { slide } from 'svelte/transition'
  import Icon from '../lib/Icon.svelte'
  import Modal from '../lib/Modal.svelte'
  import { ui, attempt, fail, refresh, toast } from '../lib/state.svelte'
  import { bytes } from '../lib/fmt'
  import { Devices, AddDevice, DismissDevice, RemoveDevice, CopyText, PasteText } from '../../wailsjs/go/main/App'
  import type { main } from '../../wailsjs/go/models'

  let v = $state<main.DevicesView | null>(null)
  let id = $state('')
  let name = $state('')
  let linking = $state(false)
  let removeFor = $state<main.DeviceView | null>(null)
  let showQR = $state(false)

  async function load() {
    try { v = await Devices() } catch (e) { if (ui.overview?.syncthing.running) fail(e) }
  }
  $effect(() => { ui.tick; load() })

  async function link(devId = id, devName = name) {
    linking = true
    if (await attempt(() => AddDevice(devId, devName), 'Linked. Saves will start syncing in a moment.')) {
      id = ''; name = ''
      load(); refresh()
    }
    linking = false
  }

  async function paste() {
    const t = (await PasteText())?.trim()
    if (t) id = t
  }

  function copy() {
    if (!v) return
    CopyText(v.myID)
    toast('ID copied', 'ok')
  }

  const groups = $derived(v?.myID.split('-') ?? [])
</script>

<header>
  <h1>Devices</h1>
  <p class="muted">Link your PCs once. From then on every game save follows you.</p>
</header>

{#if !ui.overview?.syncthing.running}
  <div class="card empty"><Icon name="alert" size={22} /><p>Sync isn't running. Start it from the Overview.</p></div>
{:else}
  {#each v?.pending ?? [] as p (p.id)}
    <div class="card request" transition:slide={{ duration: 160 }}>
      <div class="ic"><Icon name="devices" /></div>
      <div class="grow">
        <h2>{p.name || 'A PC'} wants to link</h2>
        <p class="mono faint ellipsis">{p.id}</p>
      </div>
      <button class="btn ghost" onclick={async () => { await attempt(() => DismissDevice(p.id)); load(); refresh() }}>Ignore</button>
      <button class="btn primary" disabled={linking} onclick={() => link(p.id, p.name)}>Accept</button>
    </div>
  {/each}

  <section class="grid">
    <div class="card me">
      <div class="row">
        <h2 class="grow">This PC</h2>
        <span class="muted">{v?.myName}</span>
      </div>
      <p class="muted small">Enter this ID on your other PC.</p>
      <button class="idbox selectable" onclick={copy} title="Copy">
        {#each groups as g, i}<span>{g}</span>{#if i < groups.length - 1}<span class="sep">-</span>{/if}{/each}
      </button>
      <div class="row">
        <button class="btn" onclick={copy}><Icon name="copy" size={15} /> Copy ID</button>
        <button class="btn ghost" onclick={() => (showQR = true)}>Show QR</button>
      </div>
    </div>

    <div class="card">
      <h2>Link another PC</h2>
      <ol class="muted small">
        <li>Install and open Syncer on the other PC.</li>
        <li>Paste its ID below — or paste this PC's ID over there.</li>
        <li>Accept the request on the other PC. Done.</li>
      </ol>
      <div class="field">
        <input type="text" class="mono" placeholder="XXXXXXX-XXXXXXX-…" bind:value={id} spellcheck="false" />
        <button class="btn icon" title="Paste" onclick={paste}><Icon name="paste" size={16} /></button>
      </div>
      <div class="row">
        <input type="text" class="grow" placeholder="Name (optional), e.g. Laptop" bind:value={name} />
        <button class="btn primary" disabled={!id.trim() || linking} onclick={() => link()}>
          {#if linking}<Icon name="refresh" size={15} class="spin" />{:else}<Icon name="link" size={15} />{/if}
          Link
        </button>
      </div>
    </div>
  </section>

  {#if v?.devices?.length}
    <h2 class="section">Linked PCs</h2>
    <div class="card flush list">
      {#each v.devices as d (d.id)}
        <div class="item" transition:slide={{ duration: 150 }}>
          <span class="dot {d.connected ? 'ok' : ''}"></span>
          <div class="grow">
            <div class="name">{d.name || d.id.slice(0, 7)}</div>
            <div class="faint small ellipsis">
              {#if d.connected}Online{d.address ? ` · ${d.address.replace(/^[a-z]+:\/\//, '')}` : ''}{:else}Offline — syncs when both PCs are on{/if}
            </div>
          </div>
          {#if d.connected}
            {#if d.completion >= 100}<span class="pill ok">In sync</span>
            {:else}<span class="pill accent">{Math.floor(d.completion)}% · {bytes(d.needBytes)} left</span>{/if}
          {/if}
          <button class="btn ghost icon sm danger" title="Unlink" onclick={() => (removeFor = d)}><Icon name="trash" size={16} /></button>
        </div>
      {/each}
    </div>
  {/if}
{/if}

{#if showQR && v}
  <Modal title="This PC's ID" onclose={() => (showQR = false)}>
    <img class="qr" src={v.qr} alt="QR code of device ID" />
    <p class="mono center selectable">{v.myID}</p>
    {#snippet actions()}<button class="btn" onclick={() => (showQR = false)}>Close</button>{/snippet}
  </Modal>
{/if}

{#if removeFor}
  <Modal title="Unlink {removeFor.name || 'this PC'}?" onclose={() => (removeFor = null)}>
    <p>Saves stop syncing with that PC. Nothing is deleted on either side.</p>
    {#snippet actions()}
      <button class="btn" onclick={() => (removeFor = null)}>Cancel</button>
      <button class="btn primary" onclick={async () => { const d = removeFor!; removeFor = null; await attempt(() => RemoveDevice(d.id), 'Unlinked'); load(); refresh() }}>Unlink</button>
    {/snippet}
  </Modal>
{/if}

<style>
  header { display: flex; flex-direction: column; gap: 4px; }
  .grid { display: grid; grid-template-columns: 1fr 1fr; gap: 14px; }
  .grid .card { display: flex; flex-direction: column; gap: 12px; }
  .small { font-size: 13px; }
  .idbox {
    font-family: var(--mono); font-size: 13px; line-height: 1.7; text-align: left;
    padding: 12px 14px; border-radius: 8px; border: 1px dashed var(--border);
    background: var(--surface-2); color: var(--text); cursor: pointer; word-break: break-all;
    transition: border-color .15s;
  }
  .idbox:hover { border-color: var(--accent); }
  .sep { color: var(--faint); }
  ol { margin: 0; padding-left: 18px; display: flex; flex-direction: column; gap: 3px; }
  .field { display: flex; gap: 8px; }
  .field input { flex: 1; min-width: 0; }
  .request { display: flex; align-items: center; gap: 14px; border-color: var(--accent); }
  .request .mono { font-size: 11.5px; max-width: 440px; }
  .ic {
    width: 38px; height: 38px; border-radius: 10px; flex: none; display: grid; place-items: center;
    background: var(--accent-soft); color: var(--accent);
  }
  .section { margin-top: 6px; }
  .name { font-weight: 500; }
  .empty { display: flex; flex-direction: column; align-items: center; gap: 12px; padding: 40px; color: var(--muted); }
  .qr { width: 220px; height: 220px; align-self: center; border-radius: 8px; background: #fff; padding: 8px; image-rendering: pixelated; }
  .center { text-align: center; font-size: 11.5px; word-break: break-all; }
  @media (max-width: 860px) { .grid { grid-template-columns: 1fr; } }
</style>
