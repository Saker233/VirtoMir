<script lang="ts">
  import { t } from '../i18n.svelte'
  import Monitor from '@lucide/svelte/icons/monitor'
  import LayoutTemplate from '@lucide/svelte/icons/layout-template'
  import Box from '@lucide/svelte/icons/box'
  import type { Domain } from '../api'
  import { ACTIONS, isActive, live } from '../live.svelte'
  import { formatKiB, formatPercent, plural } from '../format'
  import { machineHref, router } from '../router.svelte'
  import StatePill from './StatePill.svelte'
  import Sparkline from './Sparkline.svelte'
  import MachineActions from './MachineActions.svelte'

  let { d, selected = false, onselect, hostName }: { d: Domain; selected?: boolean; onselect?: (on: boolean) => void; hostName?: string } = $props()

  let pending = $derived(live.pending[d.uuid])
  let active = $derived(isActive(d.state))

  let memLabel = $derived(
    d.memoryKiB === d.maxMemoryKiB
      ? formatKiB(d.memoryKiB)
      : `${formatKiB(d.memoryKiB)} / ${formatKiB(d.maxMemoryKiB)}`,
  )
</script>

<div class="row" class:off={!active && d.state !== 'paused'} class:selected role="row">
  <div class="name" role="cell">
    {#if onselect}
      <input class="sel" type="checkbox" checked={selected} aria-label={t('Select {name}', { name: d.name })} onchange={(e) => onselect(e.currentTarget.checked)} />
    {/if}
    <span class="avatar" class:on={active} title={d.template ? 'Template' : d.kind === 'container' ? 'Container' : undefined}>
      {#if d.template}
        <LayoutTemplate size={16} />
      {:else if d.kind === 'container'}
        <Box size={16} />
      {:else}
        <Monitor size={16} />
      {/if}
    </span>
    <div class="titles">
      <div class="title-line">
        <a class="title" href={machineHref(d.uuid)} onclick={router.link}>{d.name}</a>
        {#if d.kind === 'container'}
          <span class="kind-badge lxc" title={t('System Container (LXC)')}>LXC</span>
        {/if}
        {#if hostName}
          <span class="host-badge" title={t('Running on {host}', { host: hostName })}>
            <span class="host-dot"></span>{hostName}
          </span>
        {/if}
      </div>
      <span class="uuid mono" title={d.uuid}>{d.uuid.slice(0, 8)}{#if !d.persistent} {t('· transient')}{/if}{#if d.autostart} {t('· autostart')}{/if}{#if d.template} {t('· template')}{:else if d.linkedFrom} {t('· linked clone')}{/if}</span>
    </div>
  </div>

  <div role="cell"><StatePill state={d.state} progress={pending ? ACTIONS[pending.action].progress : undefined} /></div>

  <div class="metric num" role="cell" title={t('Virtual CPUs')}>{plural(d.vcpus, 'vCPU')}</div>

  <div class="metric num" role="cell" title={d.memoryKiB === d.maxMemoryKiB ? 'Memory' : t('Current / maximum memory (balloon)')}>{memLabel}</div>

  <div class="cpu" role="cell">
    {#if active}
      <div class="cpu-chart"><Sparkline data={live.domainCpu(d.uuid)} tick={live.tick} height={28} /></div>
      <span class="cpu-val mono">{formatPercent(d.cpuPercent)}<small>%</small></span>
    {:else}
      <span class="cpu-idle">—</span>
    {/if}
  </div>

  <div class="actions" role="cell"><MachineActions {d} /></div>
</div>

<style>
  .row {
    display: grid;
    grid-template-columns: var(--cols);
    align-items: center;
    gap: 16px;
    min-height: 64px;
    padding: 0 16px;
    border-top: 1px solid var(--border);
    transition: background var(--dur-fast);
  }
  .row:hover {
    background: var(--surface-hover);
  }
  .row.selected {
    background: var(--accent-soft);
  }
  .sel {
    position: relative;
    z-index: 1; /* above the name link's click area */
    flex: none;
    width: 16px;
    height: 16px;
    margin: 0;
    accent-color: var(--accent);
    cursor: pointer;
  }
  .name {
    display: flex;
    align-items: center;
    gap: 12px;
    min-width: 0;
  }
  .avatar {
    flex: none;
    display: grid;
    place-items: center;
    width: 34px;
    height: 34px;
    border-radius: var(--radius-sm);
    background: var(--surface-2);
    color: var(--text-3);
    transition:
      color var(--dur),
      background var(--dur);
  }
  .avatar.on {
    background: var(--accent-soft);
    color: var(--accent);
  }
  .titles {
    display: flex;
    flex-direction: column;
    min-width: 0;
  }
  .title {
    font-weight: 600;
    font-size: 14.5px;
    color: var(--text);
    text-decoration: none;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .title-line {
    display: flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
  }
  .host-badge {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    font-size: 10px;
    font-weight: 700;
    text-transform: uppercase;
    background: var(--surface-2);
    border: 1px solid var(--border);
    color: var(--accent);
    padding: 1px 6px;
    letter-spacing: 0.05em;
    flex-shrink: 0;
  }
  .kind-badge {
    display: inline-flex;
    align-items: center;
    padding: 1px 5px;
    border-radius: var(--radius-xs);
    font-size: 10px;
    font-weight: 700;
    text-transform: uppercase;
    letter-spacing: 0.5px;
    flex-shrink: 0;
  }
  .kind-badge.lxc {
    background: rgba(14, 165, 233, 0.15);
    color: #38bdf8;
    border: 1px solid rgba(56, 189, 248, 0.3);
  }
  .host-dot {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: var(--ok);
  }
  .title:hover {
    color: var(--accent);
  }
  /* The whole name cell is clickable, not just the text. */
  .title::after {
    content: '';
    position: absolute;
    inset: 0;
  }
  .name {
    position: relative;
  }
  .uuid {
    font-size: 11.5px;
    color: var(--text-3);
  }
  .metric {
    color: var(--text-2);
    white-space: nowrap;
  }
  .off .metric {
    color: var(--text-3);
  }
  .cpu {
    display: flex;
    align-items: center;
    gap: 10px;
  }
  .cpu-chart {
    flex: 1;
    min-width: 0;
  }
  .cpu-val {
    width: 48px;
    text-align: right;
    font-size: 13px;
  }
  .cpu-val small {
    color: var(--text-3);
    margin-left: 1px;
  }
  .cpu-idle {
    color: var(--text-3);
  }
  .actions {
    display: flex;
    align-items: center;
    justify-content: flex-end;
    gap: 6px;
  }
</style>
