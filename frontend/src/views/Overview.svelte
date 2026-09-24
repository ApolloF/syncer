<script lang="ts">
  import Icon from '../lib/Icon.svelte'
  import { ui, attempt, refresh } from '../lib/state.svelte'
  import { ago } from '../lib/fmt'
  import { InstallSyncthing, StartSyncthing, BackupNow } from '../../wailsjs/go/main/App'
  import { BrowserOpenURL } from '../../wailsjs/runtime/runtime'

  const o = $derived(ui.overview)
  let busy = $state('')

  async function run(key: string, fn: () => Promise<unknown>, ok?: string) {
    busy = key
    await attempt(fn, ok)
    busy = ''
    refresh()
  }

  const steps = $derived.by(() => {
    if (!o) return []
    const s: { key: string; title: string; text: string; action?: string; icon: string }[] = []
    if (!o.syncthing.installed)
      s.push({ key: 'install', icon: 'sync', title: 'Install the sync engine', text: 'Syncer uses Syncthing to move saves between your PCs directly. One click, no account.', action: 'Install' })
    else if (o.syncthing.error)
      s.push({ key: 'sterr', icon: 'alert', title: 'Can\'t talk to Syncthing', text: o.syncthing.error })
    else if (!o.syncthing.running)
      s.push({ key: 'start', icon: 'play', title: 'Sync is not running', text: 'Start Syncthing. It will also start automatically when you sign in.', action: 'Start' })
    if (!o.drive.found && !o.settings.backupRoot)
      s.push({ key: 'drive', icon: 'cloud', title: 'Connect Google Drive', text: 'Install Google Drive for desktop and sign in. Syncer backs up into it automatically.', action: 'Get Google Drive' })
    else if (o.drive.found && !o.drive.running && !o.settings.backupRoot)
      s.push({ key: 'driveoff', icon: 'cloud', title: 'Google Drive is not running', text: 'Backups are saved locally and upload once Google Drive for desktop runs again.' })
    if (o.syncthing.running && o.devices === 0)
      s.push({ key: 'link', icon: 'link', title: 'Link your other PC', text: 'Install Syncer there, then paste this PC\'s ID. Every save follows automatically.', action: 'Link a PC' })
    if (o.pending)
      s.push({ key: 'pending', icon: 'devices', title: `${o.pending} PC${o.pending > 1 ? 's' : ''} want${o.pending > 1 ? '' : 's'} to connect`, text: 'Review and accept the request.', action: 'Review' })
    return s
  })

  function act(key: string) {
    if (key === 'install') run(key, InstallSyncthing, 'Syncthing installed and running')
    else if (key === 'start') run(key, StartSyncthing, 'Sync started')
    else if (key === 'drive') BrowserOpenURL('https://www.google.com/drive/download/')
    else if (key === 'link' || key === 'pending') ui.view = 'devices'
  }

  const lb = $derived(o?.lastBackup)
  const backupState = $derived.by(() => {
    if (!o) return { kind: '', text: '' }
    if (o.backingUp) return { kind: 'accent', text: 'Running' }
    if (!o.settings.backupEnabled) return { kind: '', text: 'Off' }
    if (!lb) return { kind: 'warn', text: 'Not yet' }
    if (!lb.ok) return { kind: 'err', text: 'Issues' }
    if (!lb.folders) return { kind: 'warn', text: 'Nothing to back up' }
    return { kind: 'ok', text: 'Healthy' }
  })
</script>

<header>
  <h1>Overview</h1>
  <p class="muted">Your game saves, on every PC and safe in Google Drive.</p>
</header>

{#if steps.length}
  <section class="steps">
    {#each steps as s (s.key)}
      <div class="card step">
        <div class="ic"><Icon name={s.icon} /></div>
        <div class="grow">
          <h2>{s.title}</h2>
          <p class="muted">{s.text}</p>
        </div>
        {#if s.action}
          <button class="btn primary" disabled={busy === s.key} onclick={() => act(s.key)}>
            {#if busy === s.key}<Icon name="refresh" size={15} class="spin" />{/if}
            {s.action}
          </button>
        {/if}
      </div>
    {/each}
  </section>
{/if}

<section class="stats">
  <button class="card stat" onclick={() => (ui.view = 'games')}>
    <div class="row"><Icon name="games" /><span class="muted">Games synced</span></div>
    <div class="big">{o?.folders ?? '–'}</div>
    <div class="row">
      {#if !o?.syncthing.running}<span class="pill err">Sync off</span>
      {:else if o.errors}<span class="pill warn">{o.errors} need attention</span>
      {:else if o.syncing}<span class="pill accent">{o.syncing} syncing</span>
      {:else}<span class="pill ok">Up to date</span>{/if}
    </div>
  </button>

  <button class="card stat" onclick={() => (ui.view = 'devices')}>
    <div class="row"><Icon name="devices" /><span class="muted">Linked PCs</span></div>
    <div class="big">{o?.devices ?? '–'}</div>
    <div class="row">
      {#if o && o.devices > 0}
        <span class="pill {o.online ? 'ok' : ''}">{o.online} online</span>
      {:else}<span class="pill">Just this PC</span>{/if}
    </div>
  </button>

  <div class="card stat">
    <div class="row"><Icon name="cloud" /><span class="muted">Last backup</span></div>
    <div class="big small">{o?.backingUp ? 'Backing up…' : ago(lb?.finished)}</div>
    <div class="row">
      <span class="pill {backupState.kind}">{backupState.text}</span>
      <span class="grow"></span>
      <button class="btn sm" disabled={o?.backingUp || !o?.target}
        onclick={() => run('backup', async () => { await BackupNow(); ui.view = 'backup' })}>
        <Icon name="upload" size={15} /> Back up now
      </button>
    </div>
  </div>
</section>

<style>
  header { display: flex; flex-direction: column; gap: 4px; margin-bottom: 4px; }
  .steps { display: flex; flex-direction: column; gap: 10px; }
  .step { display: flex; align-items: center; gap: 16px; padding: 16px 18px; }
  .step p { font-size: 13px; margin-top: 2px; }
  .ic {
    width: 38px; height: 38px; border-radius: 10px; flex: none; display: grid; place-items: center;
    background: var(--accent-soft); color: var(--accent);
  }
  .stats { display: grid; grid-template-columns: repeat(3, 1fr); gap: 14px; }
  .stat {
    display: flex; flex-direction: column; gap: 10px; text-align: left;
    font: inherit; color: inherit; cursor: default;
  }
  button.stat { cursor: pointer; transition: transform .12s, box-shadow .12s; }
  button.stat:hover { transform: translateY(-1px); box-shadow: 0 6px 18px rgba(0, 0, 0, .08); }
  .stat :global(svg) { color: var(--muted); }
  .big { font-size: 34px; font-weight: 600; letter-spacing: -0.02em; line-height: 1.1; }
  .big.small { font-size: 22px; padding: 6px 0 5px; }
  @media (max-width: 860px) { .stats { grid-template-columns: 1fr 1fr; } }
</style>
