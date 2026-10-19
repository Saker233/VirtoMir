<script lang="ts" module>
  import type { Component } from 'svelte'

  export interface MenuItem {
    label: string
    icon: Component<{ size?: number }>
    run: () => void
    /** Requires a confirming second click; shown in red. */
    danger?: { confirm: string }
    hint?: string
    /** Set to the reason when the item is not allowed (e.g. missing role). */
    disabled?: string
  }
</script>

<script lang="ts">
  // "More" menu for a machine. Dangerous items need a second click instead of a modal.
  import Ellipsis from '@lucide/svelte/icons/ellipsis'

  let { items, label }: { items: MenuItem[]; label: string } = $props()

  let open = $state(false)
  let armed = $state<number | null>(null)
  let up = $state(false)
  // Fixed position next to the trigger; the menu lives on <body> so no card or stacking context can cover it.
  let pos = $state('')
  let root: HTMLDivElement
  let button: HTMLButtonElement
  let menuEl = $state<HTMLDivElement>()

  function portal(node: HTMLElement) {
    document.body.appendChild(node)
    return { destroy: () => node.remove() }
  }

  function toggle() {
    if (!open) {
      const r = button.getBoundingClientRect()
      up = window.innerHeight - r.bottom < 56 + items.length * 40
      const right = `right: ${Math.max(8, window.innerWidth - r.right)}px;`
      pos = up ? `${right} bottom: ${window.innerHeight - r.top + 6}px;` : `${right} top: ${r.bottom + 6}px;`
    }
    open = !open
    armed = null
  }

  function close() {
    open = false
    armed = null
  }

  function choose(i: number) {
    const item = items[i]
    if (item.disabled) return
    if (item.danger && armed !== i) {
      armed = i
      return
    }
    close()
    item.run()
  }

  $effect(() => {
    if (!open) return
    const onDown = (e: PointerEvent) => {
      const t = e.target as Node
      if (!root.contains(t) && !menuEl?.contains(t)) close()
    }
    // A fixed menu would drift away from its trigger on scroll or resize; close it instead.
    const onMove = () => close()
    window.addEventListener('scroll', onMove, true)
    window.addEventListener('resize', onMove)
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        close()
        button.focus()
      }
    }
    document.addEventListener('pointerdown', onDown, true)
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('pointerdown', onDown, true)
      document.removeEventListener('keydown', onKey)
      window.removeEventListener('scroll', onMove, true)
      window.removeEventListener('resize', onMove)
    }
  })
</script>

<div class="menu-root" bind:this={root}>
  <button
    bind:this={button}
    class="trigger"
    class:open
    aria-label={label}
    aria-haspopup="menu"
    aria-expanded={open}
    onclick={toggle}
  >
    <Ellipsis size={16} />
  </button>
  {#if open}
    <div class="menu" class:up role="menu" style={pos} bind:this={menuEl} use:portal>
      {#each items as item, i (item.label)}
        {@const Icon = item.icon}
        <button
          role="menuitem"
          class="item"
          class:danger={item.danger}
          class:armed={armed === i}
          onclick={() => choose(i)}
          title={item.disabled || item.hint}
          disabled={!!item.disabled}
        >
          <Icon size={15} />
          <span>{armed === i && item.danger ? item.danger.confirm : item.label}</span>
        </button>
      {/each}
    </div>
  {/if}
</div>

<style>
  .menu-root {
    position: relative;
  }
  .trigger {
    display: grid;
    place-items: center;
    width: 32px;
    height: 32px;
    border: 1px solid transparent;
    border-radius: var(--radius-sm);
    background: transparent;
    color: var(--text-2);
    cursor: pointer;
    transition:
      background var(--dur-fast),
      border-color var(--dur-fast);
  }
  .trigger:hover,
  .trigger.open {
    background: var(--metal);
    border-color: var(--border-strong);
    box-shadow: var(--bevel);
    color: var(--text);
  }
  .menu {
    position: fixed;
    z-index: 1000;
    min-width: 220px;
    padding: 3px;
    background: var(--surface);
    border: var(--frame) solid var(--border-strong);
    border-radius: var(--radius-sm);
    box-shadow:
      var(--bevel),
      var(--shadow-pop);
    animation: pop var(--dur) var(--ease-out);
    transform-origin: top right;
  }
  .menu.up {
    transform-origin: bottom right;
  }
  .item {
    display: flex;
    align-items: center;
    gap: 10px;
    width: 100%;
    height: 34px;
    padding: 0 10px;
    border: 0;
    border-radius: var(--radius-sm);
    background: transparent;
    color: var(--text);
    font-size: 13.5px;
    text-align: left;
    white-space: nowrap;
    cursor: pointer;
  }
  .item:hover:not(:disabled) {
    background: var(--accent);
    color: var(--accent-contrast);
  }
  .item:disabled {
    opacity: 0.45;
    cursor: not-allowed;
  }
  .item.danger {
    color: var(--danger);
  }
  .item.armed {
    background: var(--danger);
    color: #fff;
    font-weight: 600;
  }
  @keyframes pop {
    from {
      opacity: 0;
      transform: translateY(-4px);
    }
  }
</style>
