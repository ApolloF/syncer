<script lang="ts">
  import Modal from './Modal.svelte'
  import Icon from './Icon.svelte'
  import { attempt, fail, toast, refresh } from './state.svelte'
  import { when } from './fmt'
  import { backupLine, plural } from './folders'
  import { RestorePoints, Restore, Decisions, SwitchDecision } from '../../wailsjs/go/main/App'
  import type { main, backup } from '../../wailsjs/go/models'

  // Restores a folder from its backup: the latest one or an earlier restore
  // point, or switches a conflict decision back.
  let { folder, onclose, ondone }: { folder: main.FolderView; onclose: () => void; ondone?: () => void } = $props()

  const launcher = $derived(folder.kind === 'launcher')
  let points = $state<main.RestorePoint[]>([])
  let latestOrigin = $state<backup.Origin | null>(null)
  let decisions = $state<main.DecisionView[]>([])
  let switching = $state('')
  let point = $state(0)
  let restoring = $state(false)

  $effect(() => { load(folder.id) })

  async function load(id: string) {
    Decisions(id).then((d) => (decisions = d ?? [])).catch(() => {})
    try {
      const v = await RestorePoints(id)
      points = v?.points ?? []
      latestOrigin = v?.latest ?? null
    } catch (e) { fail(e) }
  }

  // Which PCs a restore point's saves came from, and which PC made it.
  function originText(o?: backup.Origin | null): string {
    const from = o?.from ?? [], by = o?.by ?? []
    if (!from.length) return by.length ? `backed up by ${by.join(', ')}` : ''
    const same = by.length === 0 || (by.length === from.length && by.every((b) => from.includes(b)))
    return `from ${from.join(', ')}${same ? '' : ` · backed up by ${by.join(', ')}`}`
  }

  async function switchDecision(d: main.DecisionView) {
    const f = folder
    switching = d.rel
    if (await attempt(() => SwitchDecision(f.id, d.rel), `Now using ${d.other ? `${d.other}'s` : 'the other'} version of ${d.rel}`)) {
      decisions = (await Decisions(f.id).catch(() => [])) ?? []
      refresh()
      ondone?.()
    }
    switching = ''
  }

  async function doRestore() {
    const f = folder
    restoring = true
    try {
      const n = await Restore(f.id, point)
      toast(`Restored ${plural(n, 'file')} into ${f.label}`, 'ok')
      ondone?.()
      onclose()
    } catch (e) { fail(e) }
    restoring = false
  }
</script>

<Modal title="Restore {folder.label}" {onclose}>
  {#if folder.backup || folder.points}<p class="faint small">{backupLine(folder)}</p>{/if}
  <p>Close the {launcher ? 'launcher' : 'game'} first. Your current files are kept as a restore point, so this can be undone.</p>
  {#if folder.steamCloud}<p class="small">Steam Cloud keeps these saves too: the next time the game starts through Steam, Steam uploads the restored files over its cloud copy (or asks which to keep).</p>{/if}
  {#if decisions.length}
    <h3 class="sub">Two versions you chose between</h3>
    <div class="points">
      {#each decisions as d (d.rel)}
        <div class="pt">
          <div class="grow">
            <div class="mono ellipsis" title={d.rel}>{d.rel}</div>
            <div class="faint small">{when(d.at)} · using {d.kept ? `${d.kept}'s` : 'one'} version, {d.other ? `${d.other}'s` : 'the other'} is in the history</div>
          </div>
          <button class="btn sm" disabled={!!switching} onclick={() => switchDecision(d)}
            title="Bring the other version back. The one used now goes into the history, so you can switch again.">
            {#if switching === d.rel}<Icon name="refresh" size={14} class="spin" />{/if} Use {d.other ? `${d.other}'s` : 'the other'} instead
          </button>
        </div>
      {/each}
    </div>
    <h3 class="sub">Restore {launcher ? 'all of it' : 'the whole game'}</h3>
  {/if}
  <div class="points">
    <label class="pt"><input type="radio" bind:group={point} value={0} /> <span class="grow">Latest backup</span>
      {#if originText(latestOrigin)}<span class="origin faint" title="The PCs these saves were last changed on">{originText(latestOrigin)}</span>{/if}</label>
    {#each points as p (p.at)}
      <label class="pt"><input type="radio" bind:group={point} value={p.at} /> <span class="grow">As it was before {when(p.at)}</span>
        {#if originText(p)}<span class="origin faint" title="The PCs these saves were last changed on, and the PC that made this restore point">{originText(p)}</span>{/if}</label>
    {/each}
  </div>
  {#snippet actions()}
    <button class="btn" onclick={onclose}>Cancel</button>
    <button class="btn primary" disabled={restoring} onclick={doRestore}>
      {#if restoring}<Icon name="refresh" size={15} class="spin" />{/if} Restore
    </button>
  {/snippet}
</Modal>

<style>
  .small { font-size: 12.5px; }
  .points { display: flex; flex-direction: column; gap: 2px; max-height: 260px; overflow-y: auto; }
  .pt { display: flex; align-items: center; gap: 10px; padding: 8px 10px; border-radius: 7px; color: var(--text); cursor: pointer; }
  .pt:hover { background: var(--hover); }
  .pt input { accent-color: var(--accent); }
  .pt .origin { font-size: 12px; text-align: right; }
  h3.sub { font-size: 13px; font-weight: 600; margin: 4px 0 0; }
</style>
