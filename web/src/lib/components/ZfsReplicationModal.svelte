<script lang="ts">
  import { t } from '../i18n.svelte'
  import { ent, type ZFSReplicationStatus, type ZFSReplicationSpec } from '../enterprise'
  import { session } from '../session.svelte'
  import { toasts } from '../toast.svelte'
  import { errorText } from '../api'
  import Dialog from './Dialog.svelte'
  import RefreshCw from '@lucide/svelte/icons/refresh-cw'
  import Trash from '@lucide/svelte/icons/trash'
  import Plus from '@lucide/svelte/icons/plus'
  import ArrowRight from '@lucide/svelte/icons/arrow-right'
  import Play from '@lucide/svelte/icons/play'
  import ShieldCheck from '@lucide/svelte/icons/shield-check'

  let { open = false, onclose }: { open: boolean; onclose: () => void } = $props()

  let replications = $state<ZFSReplicationStatus[]>([])
  let loading = $state(false)
  let syncingId = $state<string | null>(null)
  let showAdd = $state(false)

  let sourcePool = $state('')
  let sourceVolume = $state('')
  let targetHost = $state('')
  let targetPool = $state('')
  let intervalMinutes = $state(5)
  let creating = $state(false)
  let formError = $state('')

  async function load() {
    loading = true
    try {
      replications = await ent.zfsReplications()
    } catch (e) {
      toasts.push({ kind: 'error', title: t('Failed to load ZFS replications'), detail: errorText(e) })
    } finally {
      loading = false
    }
  }

  $effect(() => {
    if (open) {
      void load()
    }
  })

  async function syncNow(id: string) {
    syncingId = id
    try {
      await ent.syncZFSReplication(id)
      toasts.push({ kind: 'info', title: t('ZFS differential replication completed') })
      await load()
    } catch (e) {
      toasts.push({ kind: 'error', title: t('Replication failed'), detail: errorText(e) })
    } finally {
      syncingId = null
    }
  }

  async function remove(id: string) {
    if (!confirm(t('Delete this replication schedule?'))) return
    try {
      await ent.deleteZFSReplication(id)
      toasts.push({ kind: 'info', title: t('Replication schedule removed') })
      await load()
    } catch (e) {
      toasts.push({ kind: 'error', title: t('Failed to delete schedule'), detail: errorText(e) })
    }
  }

  async function createSchedule(e: SubmitEvent) {
    e.preventDefault()
    formError = ''
    creating = true
    try {
      await ent.createZFSReplication({
        sourcePool: sourcePool.trim(),
        sourceVolume: sourceVolume.trim(),
        targetHost: targetHost.trim(),
        targetPool: targetPool.trim(),
        intervalMinutes: Number(intervalMinutes),
        enabled: true,
      })
      toasts.push({ kind: 'info', title: t('ZFS replication schedule created') })
      sourceVolume = ''
      showAdd = false
      await load()
    } catch (err) {
      formError = errorText(err)
    } finally {
      creating = false
    }
  }
</script>

<Dialog
  {open}
  title={t('ZFS Asynchronous Replication (zfs send/recv)')}
  subtitle={t('Automated differential block-level storage replication between nodes')}
  {onclose}
>
  <div class="rep-container">
    <div class="rep-header">
      <div class="info-banner">
        <ShieldCheck size={18} class="banner-icon" />
        <span>
          {t('Synchronizes differential ZFS snapshots to remote target hosts every few minutes, enabling sub-minute RPO recovery without SAN/NAS.')}
        </span>
      </div>
      <div class="header-actions">
        <button class="ui-btn sm ghost" onclick={load} disabled={loading} title={t('Refresh status')}>
          <RefreshCw size={14} class={loading ? 'spin' : ''} /> {t('Refresh')}
        </button>
        {#if !showAdd}
          <button class="ui-btn sm primary" disabled={!session.can('admin')} onclick={() => (showAdd = true)}>
            <Plus size={14} /> {t('New Schedule')}
          </button>
        {/if}
      </div>
    </div>

    {#if showAdd}
      <form class="add-box ui-panel" onsubmit={createSchedule}>
        <h3>{t('Configure ZFS Differential Replication')}</h3>
        <div class="grid2">
          <label class="ui-field">
            <span class="label">{t('Source Pool')}</span>
            <input class="ui-input mono" bind:value={sourcePool} placeholder="tank" required />
          </label>
          <label class="ui-field">
            <span class="label">{t('Source Dataset / Zvol')}</span>
            <input class="ui-input mono" bind:value={sourceVolume} placeholder="vm-101-disk-0" required />
          </label>
        </div>
        <div class="grid2">
          <label class="ui-field">
            <span class="label">{t('Target Host IP / FQDN')}</span>
            <input class="ui-input mono" bind:value={targetHost} placeholder="192.168.1.50" required />
          </label>
          <label class="ui-field">
            <span class="label">{t('Target ZFS Pool')}</span>
            <input class="ui-input mono" bind:value={targetPool} placeholder="backup-pool" required />
          </label>
        </div>
        <div class="grid2">
          <label class="ui-field">
            <span class="label">{t('Sync Interval (Minutes)')}</span>
            <input class="ui-input" type="number" min="1" max="1440" bind:value={intervalMinutes} required />
          </label>
        </div>

        {#if formError}
          <div class="ui-error-box">{formError}</div>
        {/if}

        <div class="form-actions">
          <button type="button" class="ui-btn ghost" onclick={() => (showAdd = false)}>{t('Cancel')}</button>
          <button type="submit" class="ui-btn primary" disabled={creating}>
            {creating ? t('Creating…') : t('Save Schedule')}
          </button>
        </div>
      </form>
    {/if}

    {#if loading && replications.length === 0}
      <p class="muted">{t('Loading replication tasks…')}</p>
    {:else if replications.length === 0}
      <div class="empty-state">
        <p class="muted">{t('No active ZFS replication schedules.')}</p>
      </div>
    {:else}
      <div class="task-list">
        {#each replications as rep (rep.spec.id)}
          <div class="rep-card">
            <div class="rep-main">
              <div class="rep-flow">
                <span class="mono bold">{rep.spec.sourcePool}/{rep.spec.sourceVolume}</span>
                <ArrowRight size={14} class="muted" />
                <span class="mono bold">{rep.spec.targetHost}:{rep.spec.targetPool}</span>
              </div>
              <div class="rep-meta">
                <span class="badge" class:green={rep.state === 'idle'} class:sync={rep.state === 'syncing'}>
                  {rep.state}
                </span>
                <span class="muted">{t('Interval: {n}m', { n: rep.spec.intervalMinutes })}</span>
                {#if rep.lastSync}
                  <span class="muted">{t('Last synced: {time}', { time: new Date(rep.lastSync).toLocaleTimeString() })}</span>
                {/if}
              </div>
            </div>
            <div class="rep-actions" inert={!session.can('admin')}>
              <button
                class="ui-btn sm ghost"
                disabled={syncingId === rep.spec.id}
                onclick={() => syncNow(rep.spec.id!)}
                title={t('Trigger immediate differential sync')}
              >
                <Play size={14} /> {t('Sync Now')}
              </button>
              <button
                class="ui-btn sm ghost red"
                onclick={() => remove(rep.spec.id!)}
                title={t('Delete schedule')}
              >
                <Trash size={14} />
              </button>
            </div>
          </div>
        {/each}
      </div>
    {/if}
  </div>

  {#snippet footer()}
    <button class="ui-btn ghost" onclick={onclose}>{t('Close')}</button>
  {/snippet}
</Dialog>

<style>
  .rep-container {
    display: flex;
    flex-direction: column;
    gap: 16px;
    min-width: 580px;
  }
  .rep-header {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .info-banner {
    display: flex;
    align-items: flex-start;
    gap: 10px;
    padding: 10px 12px;
    border-radius: var(--radius-sm);
    background: var(--surface-2);
    border: 1px solid var(--border-subtle);
    font-size: 13px;
    color: var(--text-2);
    line-height: 1.4;
  }
  :global(.banner-icon) {
    color: var(--accent-1);
    flex-shrink: 0;
    margin-top: 2px;
  }
  .header-actions {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
  }
  .add-box {
    padding: 14px;
    display: flex;
    flex-direction: column;
    gap: 12px;
    background: var(--surface-1);
    border: 1px solid var(--border-subtle);
    border-radius: var(--radius-sm);
  }
  .add-box h3 {
    margin: 0;
    font-size: 14px;
    font-weight: 600;
  }
  .grid2 {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 10px;
  }
  .form-actions {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    margin-top: 4px;
  }
  .task-list {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .rep-card {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 12px 14px;
    border-radius: var(--radius-sm);
    background: var(--surface-2);
    border: 1px solid var(--border-subtle);
  }
  .rep-main {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .rep-flow {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 13px;
  }
  .rep-meta {
    display: flex;
    align-items: center;
    gap: 10px;
    font-size: 12px;
  }
  .badge {
    padding: 2px 6px;
    border-radius: var(--radius-xs);
    font-size: 11px;
    font-weight: 600;
    text-transform: uppercase;
    background: var(--surface-3);
    color: var(--text-2);
  }
  .badge.green {
    background: rgba(34, 197, 94, 0.15);
    color: #4ade80;
  }
  .badge.sync {
    background: rgba(59, 130, 246, 0.15);
    color: #60a5fa;
    animation: pulse 1.5s infinite;
  }
  @keyframes pulse {
    0%, 100% { opacity: 1; }
    50% { opacity: 0.5; }
  }
  .rep-actions {
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .red {
    color: var(--danger-text, #ef4444);
  }
  .empty-state {
    padding: 24px;
    text-align: center;
  }
</style>
