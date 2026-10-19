<script lang="ts">
  import { t } from '../i18n.svelte'
  import LayoutTemplate from '@lucide/svelte/icons/layout-template'
  import HardDrive from '@lucide/svelte/icons/hard-drive'
  import {
    api,
    errorText,
    type ContentItem,
    type ContentItemType,
    type ContentSyncPolicy,
    type Domain,
    type Pool,
    type Volume,
  } from '../api'
  import { live } from '../live.svelte'
  import { cluster } from '../cluster.svelte'
  import { toasts } from '../toast.svelte'
  import Dialog from './Dialog.svelte'

  interface Props {
    open: boolean
    existingItem?: ContentItem | null
    onclose?: () => void
    onpublished?: () => void
  }
  let { open = $bindable(false), existingItem = null, onclose = () => (open = false), onpublished }: Props = $props()

  let source = $state<'template' | 'volume'>('template')
  let selectedTemplate = $state('')
  let pools = $state<Pool[]>([])
  let selectedPool = $state('')
  let volumes = $state<Volume[]>([])
  let selectedVolume = $state('')
  let itemType = $state<ContentItemType>('image')

  let name = $state('')
  let version = $state('1.0')
  let description = $state('')
  let keepVersions = $state(3)
  let syncMode = $state<'all' | 'selected' | 'manual'>('all')
  let selectedHosts = $state<string[]>([])

  let busy = $state(false)
  let error = $state('')

  let templates = $derived(live.snapshot?.domains.filter((d) => d.template) ?? [])

  $effect(() => {
    if (!open) return
    error = ''
    busy = false

    if (existingItem) {
      name = existingItem.name
      description = existingItem.description ?? ''
      keepVersions = existingItem.keepVersions || 3
      syncMode = existingItem.syncPolicy?.mode || 'all'
      selectedHosts = existingItem.syncPolicy?.hosts ? [...existingItem.syncPolicy.hosts] : []
      if (existingItem.type === 'template') {
        source = 'template'
      } else {
        source = 'volume'
        itemType = existingItem.type
      }
      // Suggest next version increment
      const count = existingItem.versions?.length ?? existingItem.versionCount ?? 1
      version = `${count + 1}.0`
    } else {
      name = ''
      version = '1.0'
      description = ''
      keepVersions = 3
      syncMode = 'all'
      selectedHosts = cluster.members.map((m) => m.id)
      source = templates.length > 0 ? 'template' : 'volume'
      itemType = 'image'
    }

    // Load pools and set defaults
    void (async () => {
      try {
        pools = (await api.pools()).filter((p) => p.active)
        if (pools.length > 0 && !selectedPool) {
          selectedPool = pools.find((p) => p.name === 'default')?.name ?? pools[0].name
        }
        if (selectedPool) {
          await loadPoolVolumes(selectedPool)
        }
      } catch (e) {
        error = errorText(e)
      }
    })()

    if (templates.length > 0 && !selectedTemplate) {
      selectedTemplate = templates[0].uuid
    }
  })

  async function loadPoolVolumes(poolName: string) {
    try {
      volumes = await api.volumes(poolName)
      if (volumes.length > 0) {
        selectedVolume = volumes[0].name
        updateTypeFromVolume(volumes[0].name)
      } else {
        selectedVolume = ''
      }
    } catch {
      volumes = []
      selectedVolume = ''
    }
  }

  function onPoolChange(poolName: string) {
    selectedPool = poolName
    void loadPoolVolumes(poolName)
  }

  function updateTypeFromVolume(volName: string) {
    if (volName.toLowerCase().endsWith('.iso')) {
      itemType = 'iso'
    } else {
      itemType = 'image'
    }
  }

  function onVolumeChange(volName: string) {
    selectedVolume = volName
    updateTypeFromVolume(volName)
    if (!name && !existingItem) {
      name = volName.replace(/\.(qcow2|img|raw|iso)$/i, '')
    }
  }

  function onTemplateChange(tplUuid: string) {
    selectedTemplate = tplUuid
    const tpl = templates.find((t) => t.uuid === tplUuid)
    if (tpl && !name && !existingItem) {
      name = tpl.name
    }
  }

  function toggleHost(id: string) {
    if (selectedHosts.includes(id)) {
      selectedHosts = selectedHosts.filter((h) => h !== id)
    } else {
      selectedHosts = [...selectedHosts, id]
    }
  }

  let valid = $derived.by(() => {
    if (!name.trim() || !version.trim()) return false
    if (source === 'template' && !selectedTemplate) return false
    if (source === 'volume' && (!selectedPool || !selectedVolume)) return false
    if (syncMode === 'selected' && selectedHosts.length === 0) return false
    return true
  })

  async function submit(e: SubmitEvent) {
    e.preventDefault()
    if (!valid) return
    busy = true
    error = ''

    const policy: ContentSyncPolicy = {
      mode: syncMode,
      hosts: syncMode === 'selected' ? selectedHosts : undefined,
    }

    try {
      if (existingItem) {
        // Publish version
        if (source === 'template') {
          await api.publishContentVersion(existingItem.id, {
            templateUuid: selectedTemplate,
            version: version.trim(),
            description: description.trim() || undefined,
          })
        } else {
          await api.publishContentVersion(existingItem.id, {
            pool: selectedPool,
            volume: selectedVolume,
            version: version.trim(),
            description: description.trim() || undefined,
          })
        }
        toasts.push({
          kind: 'info',
          title: t('Version {version} published for {name}', { version: version.trim(), name: existingItem.name }),
        })
      } else {
        // Publish new item
        if (source === 'template') {
          await api.publishContent({
            templateUuid: selectedTemplate,
            name: name.trim(),
            version: version.trim(),
            description: description.trim() || undefined,
            keepVersions,
            syncPolicy: policy,
          })
        } else {
          await api.publishContent({
            pool: selectedPool,
            volume: selectedVolume,
            type: itemType,
            name: name.trim(),
            version: version.trim(),
            description: description.trim() || undefined,
            keepVersions,
            syncPolicy: policy,
          })
        }
        toasts.push({
          kind: 'info',
          title: t('Published {name} to Content Library', { name: name.trim() }),
        })
      }

      onpublished?.()
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
  title={existingItem ? t('Add version to {name}', { name: existingItem.name }) : t('Publish item to library')}
  subtitle={existingItem
    ? t('Create and distribute an updated version of this catalog item')
    : t('Share a template, ISO or cloud image across all hosts in the cluster')}
  width={540}
  {onclose}
>
  <form id="publish-content-form" class="form" onsubmit={submit}>
    {#if error}
      <div class="ui-error-box">{error}</div>
    {/if}

    {#if !existingItem}
      <!-- Source Toggle -->
      <div class="field-group">
        <span class="label-head">{t('Content source')}</span>
        <div class="seg" role="radiogroup" aria-label={t('Content source')}>
          <label class:on={source === 'template'}>
            <input type="radio" bind:group={source} value="template" />
            <LayoutTemplate size={14} />
            <span>{t('From Template')}</span>
          </label>
          <label class:on={source === 'volume'}>
            <input type="radio" bind:group={source} value="volume" />
            <HardDrive size={14} />
            <span>{t('From Storage Volume')}</span>
          </label>
        </div>
      </div>
    {/if}

    {#if source === 'template'}
      <label class="ui-field">
        <span class="label">{t('Source Template')}</span>
        {#if templates.length === 0}
          <div class="empty-hint">
            {t('No templates found on this host. Mark a machine as template first or publish from storage.')}
          </div>
        {:else}
          <select class="ui-input" bind:value={selectedTemplate} onchange={(e) => onTemplateChange(e.currentTarget.value)} required>
            {#each templates as tpl (tpl.uuid)}
              <option value={tpl.uuid}>{tpl.name} ({tpl.vcpus} vCPU, {Math.round(tpl.memoryKiB / 1024)} MiB)</option>
            {/each}
          </select>
        {/if}
      </label>
    {:else}
      <div class="row-2">
        <label class="ui-field">
          <span class="label">{t('Storage pool')}</span>
          <select class="ui-input" bind:value={selectedPool} onchange={(e) => onPoolChange(e.currentTarget.value)} required>
            {#each pools as p (p.name)}
              <option value={p.name}>{p.name} ({p.type})</option>
            {/each}
          </select>
        </label>

        <label class="ui-field">
          <span class="label">{t('Volume')}</span>
          <select class="ui-input" bind:value={selectedVolume} onchange={(e) => onVolumeChange(e.currentTarget.value)} required>
            {#if volumes.length === 0}
              <option value="" disabled>{t('No volumes found')}</option>
            {:else}
              {#each volumes as vol (vol.name)}
                <option value={vol.name}>{vol.name} ({vol.format})</option>
              {/each}
            {/if}
          </select>
        </label>
      </div>

      {#if !existingItem}
        <label class="ui-field">
          <span class="label">{t('Item type')}</span>
          <select class="ui-input" bind:value={itemType} required>
            <option value="image">{t('Cloud Image (qcow2 / raw)')}</option>
            <option value="iso">{t('Installation Media (ISO)')}</option>
          </select>
        </label>
      {/if}
    {/if}

    <div class="row-2">
      <label class="ui-field">
        <span class="label">{t('Name')}</span>
        <input
          class="ui-input"
          bind:value={name}
          disabled={!!existingItem}
          placeholder={t('e.g. Ubuntu 24.04 Base')}
          required
        />
      </label>

      <label class="ui-field">
        <span class="label">{t('Version')}</span>
        <input class="ui-input" bind:value={version} placeholder={t('1.0')} required />
      </label>
    </div>

    <label class="ui-field">
      <span class="label">{t('Description')} <span class="opt">{t('(optional)')}</span></span>
      <textarea
        class="ui-input text-area"
        bind:value={description}
        placeholder={t('Notes on OS version, installed packages, or baseline configuration…')}
        rows={2}
      ></textarea>
    </label>

    {#if !existingItem}
      <div class="row-2">
        <label class="ui-field">
          <span class="label">{t('Keep versions')}</span>
          <input class="ui-input" type="number" min={1} max={50} bind:value={keepVersions} required />
          <span class="hint">{t('Maximum number of older versions to retain in storage.')}</span>
        </label>

        <label class="ui-field">
          <span class="label">{t('Replication policy')}</span>
          <select class="ui-input" bind:value={syncMode}>
            <option value="all">{t('All Hosts (automatic)')}</option>
            <option value="selected">{t('Selected Hosts')}</option>
            <option value="manual">{t('Manual (on-demand only)')}</option>
          </select>
        </label>
      </div>

      {#if syncMode === 'selected'}
        <div class="hosts-selection">
          <span class="label-head">{t('Target cluster hosts')}</span>
          <div class="hosts-checkboxes">
            {#each cluster.members as m (m.id)}
              <label class="host-check">
                <input
                  type="checkbox"
                  checked={selectedHosts.includes(m.id)}
                  onchange={() => toggleHost(m.id)}
                />
                <span>{m.name} {m.self ? `(${t('this host')})` : ''}</span>
              </label>
            {/each}
          </div>
        </div>
      {/if}
    {/if}
  </form>

  {#snippet footer()}
    <button type="button" class="ui-btn" onclick={onclose}>{t('Cancel')}</button>
    <button
      type="submit"
      form="publish-content-form"
      class="ui-btn primary"
      disabled={!valid || busy}
    >
      {busy ? t('Publishing…') : existingItem ? t('Publish version') : t('Publish item')}
    </button>
  {/snippet}
</Dialog>

<style>
  .form {
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .field-group {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .label-head {
    font-size: 13px;
    font-weight: 600;
    color: var(--text);
  }
  .seg {
    display: flex;
    gap: 2px;
    background: var(--bg-elev);
    padding: 3px;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
  }
  .seg label {
    flex: 1;
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 8px;
    padding: 7px 12px;
    font-size: 13px;
    font-weight: 500;
    color: var(--text-2);
    cursor: pointer;
    border-radius: var(--radius-xs);
    transition: background var(--fast), color var(--fast);
  }
  .seg label input {
    position: absolute;
    opacity: 0;
    pointer-events: none;
  }
  .seg label.on {
    background: var(--surface);
    color: var(--text);
    box-shadow: var(--shadow-sm);
  }
  .row-2 {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 12px;
  }
  .text-area {
    resize: vertical;
    min-height: 54px;
  }
  .opt {
    font-weight: normal;
    color: var(--text-3);
    font-size: 11px;
  }
  .hint {
    font-size: 11.5px;
    color: var(--text-3);
    margin-top: 2px;
  }
  .empty-hint {
    font-size: 12.5px;
    padding: 10px 12px;
    background: var(--surface-2);
    border: 1px dashed var(--border);
    color: var(--text-2);
    border-radius: var(--radius-sm);
  }
  .hosts-selection {
    display: flex;
    flex-direction: column;
    gap: 6px;
    padding: 10px 12px;
    background: var(--surface-2);
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
  }
  .hosts-checkboxes {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 8px;
    margin-top: 4px;
  }
  .host-check {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 12.5px;
    color: var(--text);
    cursor: pointer;
  }
</style>
