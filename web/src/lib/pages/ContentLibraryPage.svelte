<script lang="ts">
  import { t } from '../i18n.svelte'
  import BookOpen from '@lucide/svelte/icons/book-open'
  import Disc from '@lucide/svelte/icons/disc'
  import Layers from '@lucide/svelte/icons/layers'
  import Plus from '@lucide/svelte/icons/plus'
  import Settings from '@lucide/svelte/icons/settings'
  import Search from '@lucide/svelte/icons/search'
  import Rocket from '@lucide/svelte/icons/rocket'
  import RefreshCw from '@lucide/svelte/icons/refresh-cw'
  import Trash from '@lucide/svelte/icons/trash'
  import Copy from '@lucide/svelte/icons/copy'
  import Check from '@lucide/svelte/icons/check'
  import ChevronDown from '@lucide/svelte/icons/chevron-down'
  import Cloud from '@lucide/svelte/icons/cloud'
  import AlertTriangle from '@lucide/svelte/icons/triangle-alert'

  import { api, errorText, type ContentItem, type ContentVersion } from '../api'
  import { cluster } from '../cluster.svelte'
  import { formatBytes } from '../format'
  import { track } from '../jobs'
  import { toasts } from '../toast.svelte'
  import ConfirmButton from '../components/ConfirmButton.svelte'
  import PublishContentDialog from '../components/PublishContentDialog.svelte'
  import DeployContentDialog from '../components/DeployContentDialog.svelte'
  import ContentSubscriptionDialog from '../components/ContentSubscriptionDialog.svelte'

  let items = $state<ContentItem[]>([])
  let loading = $state(true)
  let error = $state('')

  type FilterType = 'all' | 'template' | 'iso' | 'image'
  let filter = $state<FilterType>('all')
  let searchQuery = $state('')

  let expandedIds = $state<Set<string>>(new Set())
  let copiedHash = $state<string | null>(null)
  let copyTimeout: ReturnType<typeof setTimeout> | undefined

  // Dialogs state
  let publishDlg = $state(false)
  let targetItem = $state<ContentItem | null>(null)
  let deployDlg = $state(false)
  let deployItem = $state<ContentItem | null>(null)
  let subDlg = $state(false)

  async function loadItems() {
    try {
      loading = true
      items = await api.contentItems()
      error = ''
    } catch (e) {
      error = errorText(e)
    } finally {
      loading = false
    }
  }

  $effect(() => {
    void loadItems()
  })

  let filtered = $derived.by(() => {
    let list = items
    if (filter !== 'all') {
      list = list.filter((it) => it.type === filter)
    }
    const q = searchQuery.trim().toLowerCase()
    if (q) {
      list = list.filter(
        (it) =>
          it.name.toLowerCase().includes(q) ||
          (it.description && it.description.toLowerCase().includes(q)) ||
          it.type.toLowerCase().includes(q) ||
          (it.latestVersion && it.latestVersion.version.toLowerCase().includes(q)),
      )
    }
    return list
  })

  function toggleExpand(id: string) {
    const next = new Set(expandedIds)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    expandedIds = next
  }

  function copySHA(sha: string) {
    if (!sha) return
    navigator.clipboard.writeText(sha)
    copiedHash = sha
    clearTimeout(copyTimeout)
    copyTimeout = setTimeout(() => (copiedHash = null), 2000)
    toasts.push({ kind: 'info', title: t('SHA-256 checksum copied to clipboard') })
  }

  function resolveHostName(hostId: string): string {
    if (!hostId) return 'local'
    const m = cluster.members.find((member) => member.id === hostId)
    return m ? m.name : hostId
  }

  function openPublishNew() {
    targetItem = null
    publishDlg = true
  }

  function openAddVersion(item: ContentItem) {
    targetItem = item
    publishDlg = true
  }

  function openDeploy(item: ContentItem) {
    deployItem = item
    deployDlg = true
  }

  async function syncItem(item: ContentItem) {
    try {
      const job = await api.syncContent(item.id)
      track(job, {
        success: t('Synchronized {name}', { name: item.name }),
        onSuccess: () => void loadItems(),
      })
      toasts.push({ kind: 'info', title: t('Sync job started for {name}', { name: item.name }) })
      // Temporarily mark as syncing in UI
      item.syncStatus = 'syncing'
    } catch (e) {
      toasts.push({ kind: 'error', title: t('Sync failed'), detail: errorText(e) })
    }
  }

  async function deleteItem(id: string) {
    await api.deleteContent(id)
    toasts.push({ kind: 'info', title: t('Item removed from library') })
    await loadItems()
  }

  async function deleteVersion(itemId: string, vid: string) {
    await api.deleteContentVersion(itemId, vid)
    toasts.push({ kind: 'info', title: t('Version removed') })
    await loadItems()
  }

  function getLatest(item: ContentItem): ContentVersion | undefined {
    if (item.latestVersion) return item.latestVersion
    if (item.versions && item.versions.length > 0) {
      return item.versions[item.versions.length - 1]
    }
    return undefined
  }
</script>

<svelte:head><title>{t('Content Library · VirtoMir')}</title></svelte:head>

<header class="page-head">
  <div>
    <div class="title-row">
      <BookOpen size={22} />
      <h1>{t('Content Library')}</h1>
      <span class="count-badge num">
        {filtered.length} {filtered.length === 1 ? t('item') : t('items')}
      </span>
    </div>
    <p class="lead">{t('Cluster-wide catalog of templates, ISOs, and cloud images')}</p>
  </div>
  <div class="head-right">
    <button class="ui-btn" onclick={() => (subDlg = true)} title={t('Configure local storage pool and replication')}>
      <Settings size={15} /> {t('Subscription settings')}
    </button>
    <button class="ui-btn primary" onclick={openPublishNew}>
      <Plus size={15} /> {t('Publish item')}
    </button>
  </div>
</header>

<div class="toolbar">
  <div class="pills" role="tablist" aria-label={t('Filter content')}>
    <button
      class="pill"
      class:active={filter === 'all'}
      role="tab"
      aria-selected={filter === 'all'}
      onclick={() => (filter = 'all')}
    >
      {t('All')}
    </button>
    <button
      class="pill"
      class:active={filter === 'template'}
      role="tab"
      aria-selected={filter === 'template'}
      onclick={() => (filter = 'template')}
    >
      <Layers size={13} /> {t('Templates')}
    </button>
    <button
      class="pill"
      class:active={filter === 'iso'}
      role="tab"
      aria-selected={filter === 'iso'}
      onclick={() => (filter = 'iso')}
    >
      <Disc size={13} /> {t('ISOs')}
    </button>
    <button
      class="pill"
      class:active={filter === 'image'}
      role="tab"
      aria-selected={filter === 'image'}
      onclick={() => (filter = 'image')}
    >
      <BookOpen size={13} /> {t('Cloud Images')}
    </button>
  </div>

  <div class="search-wrap">
    <Search size={14} class="search-icon" />
    <input
      type="search"
      class="ui-input search-input"
      placeholder={t('Search library…')}
      bind:value={searchQuery}
    />
  </div>
</div>

{#if error}
  <div class="ui-error-box">{error}</div>
{:else if loading && items.length === 0}
  <div class="skeleton-list">
    <div class="sk-card"></div>
    <div class="sk-card"></div>
    <div class="sk-card"></div>
  </div>
{:else if filtered.length === 0}
  <div class="empty-deck ui-panel">
    <BookOpen size={36} strokeWidth={1.5} />
    <h2>{t('No items in Content Library')}</h2>
    <p>
      {#if searchQuery || filter !== 'all'}
        {t('No catalog items match your search or filter.')}
      {:else}
        {t('Publish templates or storage images to make them accessible across your cluster.')}
      {/if}
    </p>
    {#if !searchQuery && filter === 'all'}
      <button class="ui-btn primary" onclick={openPublishNew}>
        <Plus size={15} /> {t('Publish first item')}
      </button>
    {/if}
  </div>
{:else}
  <div class="items-grid">
    {#each filtered as item (item.id)}
      {@const latest = getLatest(item)}
      {@const isExpanded = expandedIds.has(item.id)}
      {@const allVers = item.versions ?? (latest ? [latest] : [])}
      <article class="item-card ui-panel">
        <div class="card-main">
          <!-- Top Row: Type & Sync Badges -->
          <div class="card-header">
            <div class="badges-left">
              {#if item.type === 'template'}
                <span class="type-badge template">
                  <Layers size={13} /> {t('Template')}
                </span>
              {:else if item.type === 'iso'}
                <span class="type-badge iso">
                  <Disc size={13} /> {t('ISO')}
                </span>
              {:else}
                <span class="type-badge image">
                  <BookOpen size={13} /> {t('Cloud Image')}
                </span>
              {/if}

              {#if latest}
                <span class="version-tag mono">v{latest.version}</span>
              {/if}
            </div>

            <div class="badges-right">
              {#if item.syncStatus === 'ready'}
                <span class="sync-badge synced" title={t('Present and verified in local storage pool')}>
                  <Check size={12} /> {t('Synced')}
                </span>
              {:else if item.syncStatus === 'syncing'}
                <span class="sync-badge syncing" title={t('Replication in progress')}>
                  <span class="pulse-indicator"></span> {t('Syncing')}
                </span>
              {:else if item.syncStatus === 'error'}
                <span class="sync-badge error" title={t('Sync failed')}>
                  <AlertTriangle size={12} /> {t('Error')}
                </span>
              {:else}
                <span class="sync-badge remote" title={t('Stored on source host; available on demand')}>
                  <Cloud size={12} /> {t('Remote')}
                </span>
              {/if}
            </div>
          </div>

          <!-- Title and Description -->
          <div class="card-body">
            <h2 class="item-name">{item.name}</h2>
            {#if item.description}
              <p class="item-desc">{item.description}</p>
            {/if}
          </div>

          <!-- Metadata row -->
          <div class="card-meta">
            {#if latest}
              <div class="meta-item">
                <span class="meta-label">{t('Size')}:</span>
                <span class="meta-val num">{formatBytes(latest.size)}</span>
              </div>
              <div class="meta-item">
                <span class="meta-label">{t('SHA-256')}:</span>
                <span class="meta-val mono sha-val" title={latest.sha256}>
                  {latest.sha256.slice(0, 10)}…
                </span>
                <button
                  type="button"
                  class="copy-btn"
                  title={t('Copy full SHA-256 hash')}
                  onclick={() => copySHA(latest.sha256)}
                >
                  {#if copiedHash === latest.sha256}
                    <Check size={12} />
                  {:else}
                    <Copy size={12} />
                  {/if}
                </button>
              </div>
            {/if}
            <div class="meta-item">
              <span class="meta-label">{t('Source')}:</span>
              <span class="meta-val">{resolveHostName(item.sourceHost)}</span>
            </div>
          </div>

          <!-- Card Actions -->
          <div class="card-actions">
            <button class="ui-btn sm primary" onclick={() => openDeploy(item)}>
              <Rocket size={14} /> {t('Deploy')}
            </button>
            <button
              class="ui-btn sm"
              onclick={() => syncItem(item)}
              disabled={item.syncStatus === 'syncing'}
              title={t('Download copy to local storage pool')}
            >
              <RefreshCw size={14} /> {t('Sync')}
            </button>
            <button class="ui-btn sm ghost" onclick={() => openAddVersion(item)}>
              <Plus size={14} /> {t('Add version')}
            </button>
            <div class="del-wrap">
              <ConfirmButton
                label={t('Delete')}
                confirm={t('Delete item?')}
                icon={Trash}
                run={() => deleteItem(item.id)}
              />
            </div>
          </div>
        </div>

        <!-- Version History Accordion -->
        <div class="history-accordion">
          <button
            type="button"
            class="history-toggle"
            onclick={() => toggleExpand(item.id)}
            aria-expanded={isExpanded}
          >
            <span>
              {t('Version history ({count})', {
                count: allVers.length,
              })}
            </span>
            <ChevronDown size={14} class="chevron {isExpanded ? 'rotated' : ''}" />
          </button>

          {#if isExpanded}
            <div class="history-content">
              <table class="history-table">
                <thead>
                  <tr>
                    <th>{t('Version')}</th>
                    <th>{t('Date')}</th>
                    <th>{t('Size')}</th>
                    <th>{t('SHA-256')}</th>
                    <th class="actions-col"></th>
                  </tr>
                </thead>
                <tbody>
                  {#each allVers as v (v.id || v.version)}
                    <tr>
                      <td class="mono strong">v{v.version}</td>
                      <td class="num">{new Date(v.created).toLocaleDateString()}</td>
                      <td class="num">{formatBytes(v.size)}</td>
                      <td class="mono sha-col">
                        <span>{v.sha256.slice(0, 10)}…</span>
                        <button
                          type="button"
                          class="copy-btn"
                          title={t('Copy full SHA-256')}
                          onclick={() => copySHA(v.sha256)}
                        >
                          <Copy size={11} />
                        </button>
                      </td>
                      <td class="actions-col">
                        {#if allVers.length > 1}
                          <ConfirmButton
                            label={t('Delete')}
                            confirm={t('Delete version?')}
                            icon={Trash}
                            size="sm"
                            run={() => deleteVersion(item.id, v.id || v.version)}
                          />
                        {/if}
                      </td>
                    </tr>
                  {/each}
                </tbody>
              </table>
            </div>
          {/if}
        </div>
      </article>
    {/each}
  </div>
{/if}

<PublishContentDialog
  bind:open={publishDlg}
  existingItem={targetItem}
  onclose={() => (publishDlg = false)}
  onpublished={loadItems}
/>

<DeployContentDialog
  bind:open={deployDlg}
  item={deployItem}
  onclose={() => (deployDlg = false)}
  ondeployed={loadItems}
/>

<ContentSubscriptionDialog
  bind:open={subDlg}
  onclose={() => (subDlg = false)}
  onsaved={loadItems}
/>

<style>
  .title-row {
    display: flex;
    align-items: center;
    gap: 10px;
    margin-bottom: 4px;
  }
  .title-row h1 {
    margin: 0;
  }
  .count-badge {
    padding: 2px 8px;
    background: var(--surface);
    border: 1px solid var(--border);
    font-size: 11.5px;
    color: var(--text-2);
    border-radius: var(--radius-sm);
  }
  .head-right {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .toolbar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 16px;
    margin-bottom: 24px;
    flex-wrap: wrap;
  }
  .pills {
    display: flex;
    gap: 6px;
    flex-wrap: wrap;
  }
  .pill {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 6px 14px;
    background: var(--surface);
    border: 1px solid var(--border);
    color: var(--text-2);
    font-size: 12.5px;
    font-weight: 500;
    border-radius: var(--radius-sm);
    cursor: pointer;
    transition: all var(--fast);
  }
  .pill:hover {
    border-color: var(--border-strong);
    color: var(--text);
  }
  .pill.active {
    background: var(--ink);
    border-color: var(--ink);
    color: var(--surface);
  }
  .search-wrap {
    position: relative;
    min-width: 260px;
  }
  :global(.search-wrap .search-icon) {
    position: absolute;
    left: 10px;
    top: 50%;
    transform: translateY(-50%);
    color: var(--text-3);
    pointer-events: none;
  }
  .search-input {
    width: 100%;
    padding-left: 32px;
    height: 34px;
    font-size: 13px;
  }

  /* Empty state */
  .empty-deck {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    padding: 48px 24px;
    text-align: center;
    gap: 12px;
    color: var(--text-2);
  }
  .empty-deck h2 {
    margin: 0;
    font-size: 18px;
    color: var(--text);
  }
  .empty-deck p {
    margin: 0 0 8px;
    max-width: 440px;
    font-size: 13.5px;
    color: var(--text-3);
  }

  /* Grid */
  .items-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(440px, 1fr));
    gap: 20px;
  }
  .item-card {
    display: flex;
    flex-direction: column;
    background: var(--surface);
    border: var(--frame) solid var(--border);
    border-radius: var(--radius-sm);
    overflow: hidden;
    box-shadow: var(--shadow-sm);
  }
  .card-main {
    padding: 16px 18px 14px;
    display: flex;
    flex-direction: column;
    gap: 12px;
    flex: 1;
  }
  .card-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 10px;
  }
  .badges-left,
  .badges-right {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .type-badge {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    font-size: 11.5px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.03em;
    padding: 3px 8px;
    border-radius: var(--radius-xs);
    border: 1px solid transparent;
  }
  .type-badge.template {
    background: color-mix(in srgb, var(--accent) 12%, var(--surface));
    border-color: color-mix(in srgb, var(--accent) 30%, transparent);
    color: var(--accent);
  }
  .type-badge.iso {
    background: color-mix(in srgb, var(--ochre) 14%, var(--surface));
    border-color: color-mix(in srgb, var(--ochre) 35%, transparent);
    color: var(--ochre);
  }
  .type-badge.image {
    background: color-mix(in srgb, var(--blue, #3b82f6) 12%, var(--surface));
    border-color: color-mix(in srgb, var(--blue, #3b82f6) 30%, transparent);
    color: var(--blue, #3b82f6);
  }

  .version-tag {
    font-size: 11.5px;
    padding: 2px 6px;
    background: var(--bg-elev);
    border: 1px solid var(--border);
    border-radius: var(--radius-xs);
    color: var(--text-2);
  }

  /* Sync badges */
  .sync-badge {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    font-size: 11.5px;
    font-weight: 600;
    padding: 2px 7px;
    border-radius: var(--radius-xs);
    border: 1px solid var(--border);
  }
  .sync-badge.synced {
    background: color-mix(in srgb, var(--ok) 12%, var(--surface));
    border-color: color-mix(in srgb, var(--ok) 40%, transparent);
    color: var(--ok);
  }
  .sync-badge.syncing {
    background: color-mix(in srgb, var(--accent) 12%, var(--surface));
    border-color: color-mix(in srgb, var(--accent) 40%, transparent);
    color: var(--accent);
  }
  .pulse-indicator {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: var(--accent);
    animation: pulse 1.4s ease-in-out infinite;
  }
  @keyframes pulse {
    0%,
    100% {
      opacity: 0.3;
      transform: scale(0.8);
    }
    50% {
      opacity: 1;
      transform: scale(1.2);
    }
  }
  .sync-badge.remote {
    background: var(--surface);
    border-color: var(--border);
    color: var(--text-3);
  }
  .sync-badge.error {
    background: color-mix(in srgb, var(--danger, #ff4d4f) 12%, var(--surface));
    border-color: color-mix(in srgb, var(--danger, #ff4d4f) 40%, transparent);
    color: var(--danger, #ff4d4f);
  }

  .item-name {
    margin: 0;
    font-size: 16px;
    font-weight: 700;
    color: var(--text);
  }
  .item-desc {
    margin: 4px 0 0;
    font-size: 12.5px;
    color: var(--text-2);
    line-height: 1.45;
  }

  /* Metadata */
  .card-meta {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 14px;
    padding: 8px 10px;
    background: var(--surface-2);
    border: 1px solid var(--border);
    border-radius: var(--radius-xs);
    font-size: 12px;
  }
  .meta-item {
    display: flex;
    align-items: center;
    gap: 5px;
  }
  .meta-label {
    color: var(--text-3);
  }
  .meta-val {
    color: var(--text);
    font-weight: 500;
  }
  .sha-val {
    font-size: 11px;
    color: var(--text-2);
  }
  .copy-btn {
    display: inline-grid;
    place-items: center;
    background: none;
    border: none;
    padding: 2px;
    cursor: pointer;
    color: var(--text-3);
    border-radius: var(--radius-xs);
  }
  .copy-btn:hover {
    color: var(--text);
    background: var(--bg-elev);
  }

  /* Actions */
  .card-actions {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
    margin-top: 4px;
  }
  .del-wrap {
    margin-left: auto;
  }

  /* History Accordion */
  .history-accordion {
    border-top: 1px solid var(--border);
    background: var(--surface-2);
  }
  .history-toggle {
    width: 100%;
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 8px 18px;
    background: none;
    border: none;
    cursor: pointer;
    font-size: 12px;
    font-weight: 600;
    color: var(--text-2);
  }
  .history-toggle:hover {
    color: var(--text);
    background: var(--bg-elev);
  }
  :global(.history-toggle .chevron) {
    transition: transform var(--fast);
  }
  :global(.history-toggle .chevron.rotated) {
    transform: rotate(180deg);
  }

  .history-content {
    padding: 6px 18px 12px;
    border-top: 1px solid var(--border);
    background: var(--surface);
  }
  .history-table {
    width: 100%;
    border-collapse: collapse;
    font-size: 11.5px;
  }
  .history-table th {
    text-align: left;
    padding: 6px 8px;
    font-size: 11px;
    font-weight: 600;
    color: var(--text-3);
    border-bottom: 1px solid var(--border);
  }
  .history-table td {
    padding: 6px 8px;
    border-bottom: 1px solid var(--border);
    color: var(--text-2);
  }
  .history-table tr:last-child td {
    border-bottom: none;
  }
  .sha-col {
    display: flex;
    align-items: center;
    gap: 4px;
  }
  .actions-col {
    text-align: right;
  }
  .strong {
    font-weight: 600;
    color: var(--text);
  }

  /* Skeleton */
  .skeleton-list {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(440px, 1fr));
    gap: 20px;
  }
  .sk-card {
    height: 220px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    animation: pulse 1.6s ease-in-out infinite;
  }

  @media (max-width: 600px) {
    .items-grid {
      grid-template-columns: 1fr;
    }
  }
</style>
