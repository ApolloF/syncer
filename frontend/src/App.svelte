<script lang="ts">
  import { fly } from 'svelte/transition'
  import Icon from './lib/Icon.svelte'
  import { ui, init, applyTheme, type View } from './lib/state.svelte'
  import Overview from './views/Overview.svelte'
  import Games from './views/Games.svelte'
  import Devices from './views/Devices.svelte'
  import Backup from './views/Backup.svelte'
  import Settings from './views/Settings.svelte'

  init()

  const nav: { id: View; label: string; icon: string }[] = [
    { id: 'overview', label: 'Overview', icon: 'home' },
    { id: 'games', label: 'Games', icon: 'games' },
    { id: 'devices', label: 'Devices', icon: 'devices' },
    { id: 'backup', label: 'Backup', icon: 'cloud' },
    { id: 'settings', label: 'Settings', icon: 'settings' },
  ]

  $effect(() => applyTheme(ui.overview?.settings.theme ?? 'system'))

  const o = $derived(ui.overview)
  const badge = $derived<Partial<Record<View, number>>>({ devices: o?.pending ?? 0 })
  const health = $derived.by(() => {
    if (!o) return { kind: '', text: 'Loading…' }
    if (!o.syncthing.running) return { kind: 'err', text: 'Sync is off' }
    if (o.errors) return { kind: 'warn', text: `${o.errors} folder issue${o.errors > 1 ? 's' : ''}` }
    if (o.syncing) return { kind: 'warn', text: 'Syncing…' }
    return { kind: 'ok', text: 'All synced' }
  })
</script>

<div class="shell">
  <aside>
    <div class="brand">
      <div class="logo"><Icon name="sync" size={16} /></div>
      <span>Syncer</span>
    </div>
    <nav>
      {#each nav as n}
        <button class="nav" class:active={ui.view === n.id} onclick={() => (ui.view = n.id)}>
          <Icon name={n.icon} />
          <span class="grow">{n.label}</span>
          {#if badge[n.id]}<span class="badge">{badge[n.id]}</span>{/if}
        </button>
      {/each}
    </nav>
    <div class="status">
      <span class="dot {health.kind}"></span>
      <span class="muted">{health.text}</span>
    </div>
  </aside>

  <main>
    {#key ui.view}
      <div class="page" in:fly={{ y: 6, duration: 180 }}>
        {#if ui.view === 'overview'}<Overview />
        {:else if ui.view === 'games'}<Games />
        {:else if ui.view === 'devices'}<Devices />
        {:else if ui.view === 'backup'}<Backup />
        {:else}<Settings />{/if}
      </div>
    {/key}
  </main>

  <div class="toasts">
    {#each ui.toasts as t (t.id)}
      <div class="toast {t.kind}" in:fly={{ y: 12, duration: 180 }} out:fly={{ x: 20, duration: 160 }}>
        <Icon name={t.kind === 'err' ? 'alert' : 'check'} size={16} />
        <span>{t.text}</span>
      </div>
    {/each}
  </div>
</div>

<style>
  .shell { display: flex; height: 100%; }
  aside {
    width: 210px; flex: none; display: flex; flex-direction: column;
    padding: 18px 10px 14px; gap: 4px;
  }
  .brand { display: flex; align-items: center; gap: 10px; padding: 4px 10px 18px; font-weight: 600; font-size: 15px; }
  .logo {
    width: 28px; height: 28px; border-radius: 8px; display: grid; place-items: center;
    background: var(--accent); color: var(--accent-text);
  }
  nav { display: flex; flex-direction: column; gap: 2px; }
  .nav {
    display: flex; align-items: center; gap: 12px; height: 36px; padding: 0 12px;
    border: 0; border-radius: 7px; background: transparent; color: var(--muted);
    font: inherit; font-weight: 500; cursor: pointer; text-align: left; position: relative;
    transition: background .12s, color .12s;
  }
  .nav:hover { background: var(--hover); color: var(--text); }
  .nav.active { background: var(--surface); color: var(--text); box-shadow: var(--shadow); }
  .nav.active::before {
    content: ''; position: absolute; left: 0; top: 10px; bottom: 10px; width: 3px;
    border-radius: 2px; background: var(--accent);
  }
  .badge {
    min-width: 18px; height: 18px; padding: 0 5px; border-radius: 9px; font-size: 11px;
    display: grid; place-items: center; background: var(--accent); color: var(--accent-text);
  }
  .status { margin-top: auto; display: flex; align-items: center; gap: 10px; padding: 8px 12px; font-size: 13px; }

  main {
    flex: 1; min-width: 0; overflow-y: auto; overflow-x: hidden;
    background: var(--surface-2);
    border-top-left-radius: 12px; border-left: 1px solid var(--border); border-top: 1px solid var(--border);
    margin-top: 8px;
  }
  .page { padding: 28px 32px 40px; max-width: 980px; margin: 0 auto; display: flex; flex-direction: column; gap: 18px; }

  .toasts { position: fixed; right: 18px; bottom: 18px; display: flex; flex-direction: column; gap: 8px; z-index: 60; }
  .toast {
    display: flex; align-items: center; gap: 10px; max-width: 420px;
    padding: 10px 14px; border-radius: 9px; background: var(--surface); border: 1px solid var(--border);
    box-shadow: 0 8px 24px rgba(0, 0, 0, .14); font-size: 13px;
  }
  .toast.ok :global(svg) { color: var(--ok); }
  .toast.err :global(svg) { color: var(--err); }
  .toast.err { border-color: var(--err-soft); }
</style>
