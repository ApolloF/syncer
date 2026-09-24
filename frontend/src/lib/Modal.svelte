<script lang="ts">
  import type { Snippet } from 'svelte'
  import { fade, scale } from 'svelte/transition'

  let { title, onclose, children, actions }:
    { title: string; onclose: () => void; children: Snippet; actions?: Snippet } = $props()
</script>

<svelte:window onkeydown={(e) => e.key === 'Escape' && onclose()} />

<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
<div class="backdrop" transition:fade={{ duration: 120 }} onclick={onclose}>
  <div class="modal card" transition:scale={{ duration: 160, start: 0.96 }} onclick={(e) => e.stopPropagation()}
    role="dialog" aria-modal="true" aria-label={title} tabindex="-1">
    <h2>{title}</h2>
    <div class="body">{@render children()}</div>
    {#if actions}<div class="actions">{@render actions()}</div>{/if}
  </div>
</div>

<style>
  .backdrop {
    position: fixed; inset: 0; z-index: 50;
    background: rgba(0, 0, 0, 0.32);
    display: grid; place-items: center; padding: 24px;
  }
  .modal { width: min(480px, 100%); padding: 22px; box-shadow: 0 20px 50px rgba(0, 0, 0, .25); }
  .body { margin: 12px 0 0; color: var(--muted); display: flex; flex-direction: column; gap: 12px; }
  .actions { display: flex; justify-content: flex-end; gap: 8px; margin-top: 20px; }
</style>
