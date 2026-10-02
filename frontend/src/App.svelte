<script lang="ts">
  import { fly } from 'svelte/transition'
  import Icon from './lib/Icon.svelte'
  import { ui, init, applyTheme, attempt, refresh, showIssue, type View } from './lib/state.svelte'
  import { SwitchAccount } from '../wailsjs/go/main/App'
  import { pausedUntil } from './lib/fmt'
  import Overview from './views/Overview.svelte'
  import Games from './views/Games.svelte'
  import Devices from './views/Devices.svelte'
  import Backup from './views/Backup.svelte'
  import Settings from './views/Settings.svelte'
  import Accounts from './views/Accounts.svelte'

  init()

  const nav = $derived<{ id: View; label: string; icon: string }[]>([
    { id: 'overview', label: 'Overview', icon: 'home' },
    { id: 'games', label: 'Games', icon: 'games' },
    ...(ui.overview?.settings.findMods ? [{ id: 'mods' as View, label: 'Mods', icon: 'mods' }] : []),
    ...(ui.overview?.settings.accounts ? [{ id: 'accounts' as View, label: 'Accounts', icon: 'users' }] : []),
    { id: 'devices', label: 'Devices', icon: 'devices' },
    { id: 'backup', label: 'Backup', icon: 'cloud' },
    { id: 'settings', label: 'Settings', icon: 'settings' },
  ])

  // Who is playing on this PC (with accounts on).
  const accts = $derived(ui.accounts)
  const me = $derived(accts?.accounts?.find(a => a.id === accts.active))
  let picker = $state(false)
  let switching = $state(false)
  async function switchTo(id: string) {
    picker = false
    if (id === accts?.active) return
    switching = true
    const name = accts?.accounts?.find(a => a.id === id)?.name
    await attempt(() => SwitchAccount(id), `${name} is playing on this PC`)
    switching = false
    refresh()
  }

  $effect(() => applyTheme(ui.overview?.settings.theme ?? 'system'))
  // The Mods page goes away with "Find installed mods".
  $effect(() => { if (ui.view === 'mods' && ui.overview && !ui.overview.settings.findMods) ui.view = 'games' })

  const o = $derived(ui.overview)
  const badge = $derived<Partial<Record<View, number>>>({ devices: o?.pending ?? 0 })
  const health = $derived.by((): { kind: string; text: string; tip?: string } => {
    if (!o) return { kind: '', text: 'Loading…' }
    if (o.paused) return { kind: 'warn', text: `Paused until ${pausedUntil(o.settings.pausedUntil)}` }
    if (!o.syncthing.running) return { kind: 'err', text: 'Sync is off' }
    if (o.errors) {
      // Name the folder and say what's wrong; with several, the tooltip lists them.
      const issues = (o.issues ?? []).map(i => `${i.label}: ${i.problem}`)
      if (issues.length === 1) return { kind: 'warn', text: issues[0], tip: issues[0] }
      return { kind: 'warn', text: `${o.errors} folder issues`, tip: issues.join('\n') }
    }
    if (o.syncing) return { kind: 'warn', text: 'Syncing…' }
    return { kind: 'ok', text: 'All synced' }
  })
</script>

<svelte:window
  onkeydown={(e) => { if (e.key === 'Escape') picker = false }}
  onclick={(e) => { if (picker && !(e.target as Element).closest?.('.who')) picker = false }} />

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
    {#if accts && accts.accounts?.length}
      <div class="who">
        <button class="nav" title="Who's playing on this PC" disabled={switching} aria-haspopup="menu" aria-expanded={picker}
          onclick={() => (picker = !picker)}>
          <span class="adot" style="background:{me?.color || 'var(--muted)'}"></span>
          <span class="grow ellipsis">{switching ? 'Switching…' : me?.name ?? 'Choose account'}</span>
          <Icon name="chevron" size={14} />
        </button>
        {#if picker}
          <div class="menu card" role="menu" transition:fly={{ y: 6, duration: 120 }}>
            {#each accts.accounts as a (a.id)}
              <button class="nav" role="menuitemradio" aria-checked={a.id === accts.active} class:active={a.id === accts.active} onclick={() => switchTo(a.id)}>
                <span class="adot" style="background:{a.color || 'var(--muted)'}"></span><span class="grow">{a.name}</span>
              </button>
            {/each}
            <button class="nav" onclick={() => { picker = false; ui.view = 'accounts' }}><Icon name="users" size={14} /><span class="grow">Manage…</span></button>
          </div>
        {/if}
      </div>
    {/if}
    {#if o?.errors && o.issues?.length && !o.paused && o.syncthing.running}
      <!-- A folder issue leads to the folder, where it can be dealt with. -->
      <button class="status" title={`${health.tip}\n\nClick to show ${o.issues.length === 1 ? 'the folder' : 'the first one'}.`} onclick={() => showIssue(o.issues[0])}>
        <span class="dot {health.kind}"></span>
        <span class="muted clamp">{health.text}</span>
      </button>
    {:else}
      <div class="status" title={health.tip}>
        <span class="dot {health.kind}"></span>
        <span class="muted clamp">{health.text}</span>
      </div>
    {/if}
  </aside>

  <main>
    {#key ui.view}
      <div class="page" in:fly={{ y: 6, duration: 180 }}>
        {#if ui.view === 'overview'}<Overview />
        {:else if ui.view === 'games'}<Games />
        {:else if ui.view === 'mods'}<Games mode="mods" />
        {:else if ui.view === 'devices'}<Devices />
        {:else if ui.view === 'backup'}<Backup />
        {:else if ui.view === 'accounts'}<Accounts />
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
  .who { margin-top: auto; position: relative; }
  .who > .nav { width: 100%; }
  .adot { width: 10px; height: 10px; border-radius: 50%; flex: none; }
  .menu { position: absolute; bottom: 40px; left: 0; right: 0; padding: 4px; display: flex; flex-direction: column; gap: 2px; z-index: 40; }
  .who + .status { margin-top: 0; }
  .status { margin-top: auto; display: flex; align-items: center; gap: 10px; padding: 8px 12px; font-size: 13px; }
  button.status { background: none; border: 0; border-radius: 8px; text-align: left; color: inherit; font: inherit; font-size: 13px; cursor: pointer; }
  button.status:hover { background: var(--hover); }
  /* A folder issue names the folder and the problem: up to four lines, the rest in the tooltip. */
  .clamp { display: -webkit-box; -webkit-line-clamp: 4; line-clamp: 4; -webkit-box-orient: vertical; overflow: hidden; min-width: 0; }

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
