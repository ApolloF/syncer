<script lang="ts">
  import Modal from './Modal.svelte'
  import Icon from './Icon.svelte'
  import { ui, fail, toast, refresh } from './state.svelte'
  import { bytes, when } from './fmt'
  import { Conflicts, ConflictOwners, SplitGame } from '../../wailsjs/go/main/App'
  import type { conflict } from '../../wailsjs/go/models'

  // Gives every account its own saves of a game. Conflicting versions can be
  // handed to the account they belong to: that account gets the other
  // version, everyone else keeps the current one.
  let { id, label, onclose, ondone }: { id: string; label: string; onclose: () => void; ondone?: () => void } = $props()

  const accs = $derived(ui.accounts?.accounts ?? [])
  const active = $derived(ui.accounts?.active ?? '')
  let conflicts = $state<conflict.Conflict[]>([])
  let owner = $state<Record<string, string>>({}) // conflict copy -> account ("" = nobody: goes to history)
  let loaded = $state(false)
  let busy = $state(false)

  $effect(() => { load() })

  async function load() {
    try {
      const [cs, os] = await Promise.all([Conflicts(id), ConflictOwners(id).catch(() => [])])
      conflicts = cs ?? []
      const guess: Record<string, string> = {}
      for (const o of os ?? []) guess[o.copy] = o.other && o.other !== o.current ? o.other : ''
      const other = accs.find(a => a.id !== active)?.id ?? ''
      for (const c of conflicts) owner[c.copy] = guess[c.copy] || other
    } catch (e) { fail(e) }
    loaded = true
  }

  // What each account ends up with, for the summary.
  const summary = $derived(accs.map(a => ({
    a,
    others: conflicts.filter(c => owner[c.copy] === a.id).map(c => c.rel),
  })))

  async function split() {
    busy = true
    const assign: Record<string, string> = {}
    for (const c of conflicts) if (owner[c.copy]) assign[c.copy] = owner[c.copy]
    try {
      await SplitGame(id, assign)
      toast(`${label} now has separate saves for each account`, 'ok')
      refresh()
      ondone?.()
      onclose()
    } catch (e) { fail(e) }
    busy = false
  }
</script>

<Modal title="Separate saves of {label}" onclose={() => { if (!busy) onclose() }}>
  <p>
    Every account gets its own copy of this game's saves, synced to all your PCs. On each PC, the account playing there has
    its saves in the game's folder; the others wait next to it until you switch account.
    A restore point is saved first, and nothing is deleted.
  </p>
  {#if !loaded}
    <p class="faint"><Icon name="refresh" size={14} class="spin" /> Looking at the saves…</p>
  {:else if conflicts.length}
    <p class="small">These saves have two versions. Say whose the <b>other version</b> is; everyone else keeps the current one.</p>
    <div class="list">
      {#each conflicts as c (c.copy)}
        <div class="conf">
          <div class="mono ellipsis" title={c.rel}>{c.rel}</div>
          <div class="faint small">
            Current: {c.missing ? 'deleted' : `${when(Date.parse(c.modified) / 1000)} · ${bytes(c.size)}`}
            · Other{c.deviceName ? ` (from ${c.deviceName})` : ''}: {when(Date.parse(c.copyModified) / 1000)} · {bytes(c.copySize)}
          </div>
          <label class="pick">
            Other version belongs to
            <select bind:value={owner[c.copy]}>
              {#each accs as a}<option value={a.id}>{a.name}</option>{/each}
              <option value="">decide later (stays a conflict here)</option>
            </select>
          </label>
        </div>
      {/each}
    </div>
    <ul class="sum small">
      {#each summary as s}
        <li><b>{s.a.name}</b>: {s.others.length ? `the other version of ${s.others.join(', ')}, the rest as it is now` : 'the saves as they are now'}</li>
      {/each}
    </ul>
  {:else}
    <p class="small">Each account starts with a copy of the saves as they are now: {accs.map(a => a.name).join(', ')}.</p>
  {/if}
  {#if ui.accounts?.waiting?.length}
    <p class="err small">Update Syncer on {ui.accounts.waiting.join(', ')} first; until then this can't be done.</p>
  {/if}
  <p class="faint small">Close the game on every PC first. You can make it shared again later from Accounts, picking whose saves to keep.</p>
  {#if busy}<p class="faint small"><Icon name="refresh" size={13} class="spin" /> Copying the saves for each account… a big game takes a few minutes.</p>{/if}
  {#snippet actions()}
    <button class="btn" disabled={busy} onclick={onclose}>Cancel</button>
    <button class="btn primary" disabled={busy || !loaded || accs.length < 2 || !!ui.accounts?.waiting?.length} onclick={split}>
      {#if busy}<Icon name="refresh" size={15} class="spin" />{:else}<Icon name="split" size={15} />{/if} Separate saves
    </button>
  {/snippet}
</Modal>

<style>
  .small { font-size: 12.5px; }
  .list { display: flex; flex-direction: column; gap: 10px; max-height: 260px; overflow-y: auto; }
  .conf { display: flex; flex-direction: column; gap: 4px; padding: 10px; border-radius: 8px; background: var(--hover); }
  .pick { display: flex; align-items: center; gap: 8px; color: var(--text); font-size: 13px; }
  .sum { margin: 0; padding-left: 18px; display: flex; flex-direction: column; gap: 2px; }
  .err { color: var(--err); }
</style>
