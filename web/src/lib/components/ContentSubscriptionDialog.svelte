<script lang="ts">
  import { t } from '../i18n.svelte'
  import { api, errorText, type ContentHostConfig, type Pool } from '../api'
  import { toasts } from '../toast.svelte'
  import Dialog from './Dialog.svelte'

  interface Props {
    open: boolean
    onclose?: () => void
    onsaved?: () => void
  }
  let { open = $bindable(false), onclose = () => (open = false), onsaved }: Props = $props()

  let pools = $state<Pool[]>([])
  let targetPool = $state('default')
  let autoSync = $state(true)
  let busy = $state(false)
  let error = $state('')

  $effect(() => {
    if (!open) return
    error = ''
    busy = false
    void (async () => {
      try {
        const [cfg, p] = await Promise.all([
          api.contentConfig().catch(() => ({ targetPool: 'default', autoSync: true } as ContentHostConfig)),
          api.pools().catch(() => [] as Pool[]),
        ])
        pools = p.filter((x) => x.active)
        targetPool = cfg.targetPool || (pools[0]?.name ?? 'default')
        autoSync = cfg.autoSync ?? true
      } catch (e) {
        error = errorText(e)
      }
    })()
  })

  async function submit(e: SubmitEvent) {
    e.preventDefault()
    busy = true
    error = ''
    try {
      await api.saveContentConfig({
        targetPool,
        autoSync,
      })
      toasts.push({ kind: 'info', title: t('Subscription settings saved') })
      onsaved?.()
      onclose()
    } catch (e) {
      error = errorText(e)
    } finally {
      busy = false
    }
  }
</script>

<Dialog
  {open}
  title={t('Subscription settings')}
  subtitle={t('Configure local storage pool and automatic replication for content items')}
  {onclose}
>
  <form id="subscription-form" class="form" onsubmit={submit}>
    {#if error}
      <div class="ui-error-box">{error}</div>
    {/if}

    <label class="ui-field">
      <span class="label">{t('Target storage pool')}</span>
      <select class="ui-input" bind:value={targetPool} required>
        {#if pools.length === 0}
          <option value="default">default</option>
        {:else}
          {#each pools as p (p.name)}
            <option value={p.name}>{p.name} ({p.type})</option>
          {/each}
        {/if}
      </select>
      <span class="hint">{t('Storage pool where downloaded copies of library items will be stored on this host.')}</span>
    </label>

    <div class="opt-box">
      <label class="ui-switch" title={t('Automatically replicate new library items and versions to this host')}>
        <input type="checkbox" bind:checked={autoSync} />
        <span>{t('Auto-sync new items and versions')}</span>
      </label>
      <p class="hint">
        {t('When enabled, items published with cluster-wide replication policy are automatically fetched into the chosen pool.')}
      </p>
    </div>
  </form>

  {#snippet footer()}
    <button type="button" class="ui-btn" onclick={onclose}>{t('Cancel')}</button>
    <button type="submit" form="subscription-form" class="ui-btn primary" disabled={busy}>
      {busy ? t('Saving…') : t('Save settings')}
    </button>
  {/snippet}
</Dialog>

<style>
  .form {
    display: flex;
    flex-direction: column;
    gap: 16px;
  }
  .opt-box {
    display: flex;
    flex-direction: column;
    gap: 6px;
    padding: 12px;
    background: var(--surface-2);
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
  }
  .hint {
    font-size: 12px;
    color: var(--text-3);
    margin: 0;
  }
</style>
