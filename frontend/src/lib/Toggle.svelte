<script lang="ts">
  let { checked = false, onchange, label = '', disabled = false }:
    { checked?: boolean; onchange?: (v: boolean) => void; label?: string; disabled?: boolean } = $props()
</script>

<button class="toggle" class:on={checked} role="switch" aria-checked={checked} aria-label={label} {disabled}
  onclick={() => onchange?.(!checked)}>
  <span></span>
</button>

<style>
  .toggle {
    width: 38px; height: 22px; border-radius: 11px; flex: none;
    border: 1px solid var(--border); background: var(--hover);
    position: relative; cursor: pointer; padding: 0;
    transition: background .18s, border-color .18s;
  }
  .toggle span {
    position: absolute; top: 3px; left: 3px; width: 14px; height: 14px; border-radius: 50%;
    background: var(--muted); transition: transform .18s cubic-bezier(.3, .7, .4, 1), background .18s;
  }
  .toggle.on { background: var(--accent); border-color: transparent; }
  .toggle.on span { transform: translateX(16px); background: var(--accent-text); }
  .toggle:disabled { opacity: .5; cursor: default; }
</style>
