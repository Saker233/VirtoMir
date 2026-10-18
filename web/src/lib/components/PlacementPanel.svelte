<script lang="ts">
  import { t } from '../i18n.svelte'
  import CircleCheck from '@lucide/svelte/icons/circle-check'
  import TriangleAlert from '@lucide/svelte/icons/triangle-alert'
  import Plus from '@lucide/svelte/icons/plus'
  import Pencil from '@lucide/svelte/icons/pencil'
  import Layers from '@lucide/svelte/icons/layers'
  import Server from '@lucide/svelte/icons/server'
  import Save from '@lucide/svelte/icons/save'
  import Tag from '@lucide/svelte/icons/tag'
  import { cluster } from '../cluster.svelte'
  import { session } from '../session.svelte'
  import { toasts } from '../toast.svelte'
  import { errorText } from '../api'
  import ConfirmButton from './ConfirmButton.svelte'
  import Dialog from './Dialog.svelte'
  import {
    ent,
    type PlacementRule,
    type PlacementViolation,
    type PlacementKind,
    type PlacementStrength,
    type ClusterDomainSummary,
  } from '../enterprise'

  let loading = $state(true)
  let rules = $state<PlacementRule[]>([])
  let violations = $state<PlacementViolation[]>([])
  let hostLabels = $state<Record<string, Record<string, string>>>({})
  let inventory = $state<ClusterDomainSummary[]>([])

  // Labels editing state
  let newLabelKeys = $state<Record<string, string>>({})
  let newLabelValues = $state<Record<string, string>>({})
  let savingLabels = $state<Record<string, boolean>>({})

  // Rule dialog state
  let showDialog = $state(false)
  let editingId = $state<string | null>(null)
  let dialogError = $state('')
  let savingRule = $state(false)

  // Form fields
  let formName = $state('')
  let formKind = $state<PlacementKind>('vm_anti_affinity')
  let formStrength = $state<PlacementStrength>('must')
  let formDomains = $state<string[]>([])
  let formHosts = $state<string[]>([])
  let formExclude = $state(false)
  let formEnabled = $state(true)
  let vmFilter = $state('')

  let vmNameMap = $derived(new Map(inventory.map((vm) => [vm.uuid, vm.name])))
  let hostNameMap = $derived(new Map(cluster.members.map((m) => [m.id, m.name])))

  let filteredInventory = $derived(
    inventory.filter((vm) => {
      const q = vmFilter.trim().toLowerCase()
      if (!q) return true
      return vm.name.toLowerCase().includes(q) || vm.uuid.toLowerCase().includes(q) || (vm.hostName && vm.hostName.toLowerCase().includes(q))
    }),
  )

  async function loadData() {
    try {
      loading = true
      const [placementData, invData] = await Promise.all([
        ent.placement(),
        ent.clusterInventoryMachines().catch(() => [] as ClusterDomainSummary[]),
      ])
      rules = placementData.rules ?? []
      hostLabels = placementData.hostLabels ?? {}
      violations = placementData.violations ?? []
      inventory = Array.isArray(invData) ? invData : (invData as any)?.domains ?? []
    } catch (e) {
      toasts.push({ kind: 'error', title: t('Could not load placement data'), detail: errorText(e) })
    } finally {
      loading = false
    }
  }

  $effect(() => {
    void loadData()
  })

  function openCreate() {
    editingId = null
    formName = ''
    formKind = 'vm_anti_affinity'
    formStrength = 'must'
    formDomains = []
    formHosts = []
    formExclude = false
    formEnabled = true
    dialogError = ''
    vmFilter = ''
    showDialog = true
  }

  function openEdit(rule: PlacementRule) {
    editingId = rule.id
    formName = rule.name
    formKind = rule.kind
    formStrength = rule.strength ?? 'must'
    formDomains = [...rule.domains]
    formHosts = rule.hosts ? [...rule.hosts] : []
    formExclude = !!rule.exclude
    formEnabled = rule.enabled !== false
    dialogError = ''
    vmFilter = ''
    showDialog = true
  }

  function toggleDomain(uuid: string) {
    if (formDomains.includes(uuid)) {
      formDomains = formDomains.filter((u) => u !== uuid)
    } else {
      formDomains = [...formDomains, uuid]
    }
  }

  function toggleHost(hostId: string) {
    if (formHosts.includes(hostId)) {
      formHosts = formHosts.filter((h) => h !== hostId)
    } else {
      formHosts = [...formHosts, hostId]
    }
  }

  async function saveRule() {
    dialogError = ''
    if (!formName.trim()) {
      dialogError = t('Rule name is required')
      return
    }
    if (formKind === 'vm_anti_affinity' && formDomains.length < 2) {
      dialogError = t('Anti-affinity rule requires at least two VMs')
      return
    }
    if (formKind === 'vm_affinity' && formDomains.length < 1) {
      dialogError = t('Affinity rule requires at least one VM')
      return
    }
    if (formKind === 'vm_host' && formDomains.length < 1) {
      dialogError = t('At least one VM must be assigned to the rule')
      return
    }
    if (formKind === 'vm_host' && formHosts.length < 1) {
      dialogError = t('VM-host rule requires at least one host')
      return
    }

    savingRule = true
    try {
      const payload: Partial<PlacementRule> = {
        name: formName.trim(),
        kind: formKind,
        strength: formStrength,
        enabled: formEnabled,
        domains: formDomains,
        hosts: formKind === 'vm_host' ? formHosts : undefined,
        exclude: formKind === 'vm_host' ? formExclude : undefined,
      }

      if (editingId) {
        await ent.savePlacementRule(editingId, payload)
        toasts.push({ kind: 'info', title: t('Placement rule updated') })
      } else {
        await ent.addPlacementRule(payload)
        toasts.push({ kind: 'info', title: t('Placement rule created') })
      }
      showDialog = false
      await loadData()
    } catch (e: any) {
      dialogError = errorText(e)
    } finally {
      savingRule = false
    }
  }

  async function deleteRule(id: string) {
    try {
      await ent.deletePlacementRule(id)
      toasts.push({ kind: 'info', title: t('Placement rule deleted') })
      await loadData()
    } catch (e) {
      toasts.push({ kind: 'error', title: t('Could not delete placement rule'), detail: errorText(e) })
    }
  }

  function addHostLabel(hostId: string) {
    const k = (newLabelKeys[hostId] ?? '').trim().toLowerCase()
    const v = (newLabelValues[hostId] ?? '').trim().toLowerCase()
    const labelRE = /^[a-z0-9][a-z0-9._-]{0,31}$/
    if (!k || !labelRE.test(k)) {
      toasts.push({ kind: 'error', title: t('Invalid label key: must match ^[a-z0-9][a-z0-9._-]{0,31}$') })
      return
    }
    if (!v || !labelRE.test(v)) {
      toasts.push({ kind: 'error', title: t('Invalid label value: must match ^[a-z0-9][a-z0-9._-]{0,31}$') })
      return
    }

    const current = { ...(hostLabels[hostId] ?? {}) }
    current[k] = v
    hostLabels[hostId] = current
    newLabelKeys[hostId] = ''
    newLabelValues[hostId] = ''
  }

  function removeHostLabel(hostId: string, key: string) {
    const current = { ...(hostLabels[hostId] ?? {}) }
    delete current[key]
    hostLabels[hostId] = current
  }

  async function saveHostLabels(hostId: string) {
    savingLabels[hostId] = true
    try {
      await ent.setHostLabels(hostId, hostLabels[hostId] ?? {})
      toasts.push({ kind: 'info', title: t('Host labels saved') })
      await loadData()
    } catch (e) {
      toasts.push({ kind: 'error', title: t('Failed to save host labels'), detail: errorText(e) })
    } finally {
      savingLabels[hostId] = false
    }
  }
</script>

<div class="placement-deck">
  <!-- (c) Violations Panel at top -->
  <section class="ui-panel violations-card" aria-label={t('Placement violations')}>
    <div class="card-head">
      <div class="title-with-icon">
        <TriangleAlert size={18} class="viol-icon" />
        <h2>{t('Placement violations')}</h2>
      </div>
      {#if violations.length > 0}
        <span class="count-badge">{violations.length}</span>
      {/if}
    </div>

    {#if loading}
      <p class="muted">{t('Loading…')}</p>
    {:else if violations.length === 0}
      <div class="empty-ok">
        <CircleCheck size={24} />
        <div>
          <strong>{t('No placement violations')}</strong>
          <p class="muted">{t('All running virtual machines satisfy cluster placement rules.')}</p>
        </div>
      </div>
    {:else}
      <div class="violations-list">
        {#each violations as v (v.ruleId + v.domainUuids.join() + v.hostId)}
          <div class="violation-row" class:hard={v.hard}>
            <span class="sev-badge" class:hard={v.hard}>
              {v.hard ? t('HARD') : t('SOFT')}
            </span>
            <div class="v-main">
              <div class="v-top">
                <strong>{v.ruleName}</strong>
                {#if v.hostId}
                  <span class="host-pill">{t('Host')}: {hostNameMap.get(v.hostId) ?? v.hostId}</span>
                {/if}
                <span class="vm-pill">
                  {t('VMs')}: {v.domainUuids.map((u) => vmNameMap.get(u) ?? u.slice(0, 8)).join(', ')}
                </span>
              </div>
              <p class="v-msg">{v.message}</p>
            </div>
          </div>
        {/each}
      </div>
    {/if}
  </section>

  <!-- (a) Rules List -->
  <section class="ui-panel rules-card" aria-label={t('Placement rules')}>
    <header class="card-head">
      <div class="title-with-icon">
        <Layers size={18} />
        <h2>{t('Placement rules')}</h2>
      </div>
      {#if session.can('admin')}
        <button class="ui-btn sm primary" onclick={openCreate}>
          <Plus size={14} /> {t('Add rule')}
        </button>
      {/if}
    </header>

    {#if loading}
      <p class="muted">{t('Loading…')}</p>
    {:else if rules.length === 0}
      <div class="empty-panel">
        <p class="muted">{t('No placement rules defined yet.')}</p>
        {#if session.can('admin')}
          <button class="ui-btn sm" onclick={openCreate}><Plus size={14} /> {t('Add rule')}</button>
        {/if}
      </div>
    {:else}
      <div class="rules-list">
        {#each rules as r (r.id)}
          <article class="rule-row" class:disabled={!r.enabled}>
            <div class="rule-meta">
              <div class="badges-row">
                <span class="kind-tag {r.kind}">
                  {r.kind === 'vm_anti_affinity' ? t('Anti-affinity') : r.kind === 'vm_affinity' ? t('Affinity') : t('VM-Host')}
                </span>
                <span class="strength-tag" class:solid={r.strength === 'must'} class:outlined={r.strength === 'should'}>
                  {r.strength === 'must' ? t('Must') : t('Should')}
                </span>
                {#if !r.enabled}
                  <span class="disabled-pill">{t('Disabled')}</span>
                {/if}
              </div>
              <div class="rule-name">{r.name}</div>
            </div>

            <div class="rule-details">
              <div class="vms-line">
                <span class="lbl">{t('Virtual machines')}:</span>
                <span class="vals">
                  {r.domains.map((u) => vmNameMap.get(u) ?? u.slice(0, 8)).join(', ')}
                </span>
              </div>
              {#if r.kind === 'vm_host'}
                <div class="hosts-line">
                  <span class="lbl">{t('Target hosts')}:</span>
                  <span class="vals">
                    {(r.hosts ?? []).map((h) => hostNameMap.get(h) ?? h).join(', ')}
                  </span>
                  {#if r.exclude}
                    <span class="pill-excluded">{t('Excluded hosts')}</span>
                  {/if}
                </div>
              {/if}
            </div>

            {#if session.can('admin')}
              <div class="rule-actions">
                <button class="ui-btn sm ghost icon" title={t('Edit')} onclick={() => openEdit(r)}>
                  <Pencil size={14} />
                </button>
                <ConfirmButton
                  label={t('Delete')}
                  confirm={t('Delete rule {name}?', { name: r.name })}
                  failure={t('Could not delete placement rule')}
                  run={() => deleteRule(r.id)}
                />
              </div>
            {/if}
          </article>
        {/each}
      </div>
    {/if}
  </section>

  <!-- (3) Host Labels Editor -->
  <section class="ui-panel labels-card" aria-label={t('Host labels & fault domains')}>
    <header class="card-head">
      <div class="title-with-icon">
        <Tag size={18} />
        <div>
          <h2>{t('Host labels & fault domains')}</h2>
          <p class="subtitle">{t('Assign key=value labels to hosts (e.g. rack=r1, zone=zone-a) to identify failure domains.')}</p>
        </div>
      </div>
    </header>

    <div class="host-labels-grid">
      {#each cluster.members as m (m.id)}
        {@const labels = hostLabels[m.id] ?? {}}
        <div class="host-label-box">
          <div class="host-box-header">
            <Server size={16} />
            <strong>{m.name}</strong>
            {#if m.self}<span class="pill">{t('this host')}</span>{/if}
          </div>

          <div class="labels-cloud">
            {#if Object.keys(labels).length === 0}
              <span class="muted small">{t('No labels assigned')}</span>
            {:else}
              {#each Object.entries(labels) as [k, v] (k)}
                <span class="label-chip mono">
                  <span class="k">{k}</span>=<span class="v">{v}</span>
                  {#if session.can('admin')}
                    <button class="del-lbl-btn" title={t('Remove')} onclick={() => removeHostLabel(m.id, k)}>×</button>
                  {/if}
                </span>
              {/each}
            {/if}
          </div>

          {#if session.can('admin')}
            <div class="add-label-row">
              <input
                type="text"
                class="ui-input sm mono"
                placeholder={t('key')}
                bind:value={newLabelKeys[m.id]}
                aria-label={t('Key')}
              />
              <span class="eq">=</span>
              <input
                type="text"
                class="ui-input sm mono"
                placeholder={t('value')}
                bind:value={newLabelValues[m.id]}
                aria-label={t('Value')}
              />
              <button class="ui-btn sm" type="button" onclick={() => addHostLabel(m.id)}>
                <Plus size={13} /> {t('Add')}
              </button>
            </div>

            <div class="save-row">
              <button
                class="ui-btn sm primary"
                disabled={savingLabels[m.id]}
                onclick={() => saveHostLabels(m.id)}
              >
                <Save size={13} /> {savingLabels[m.id] ? t('Saving…') : t('Save labels')}
              </button>
            </div>
          {/if}
        </div>
      {/each}
    </div>
  </section>
</div>

<!-- (b) Create / Edit Dialog -->
<Dialog
  open={showDialog}
  title={editingId ? t('Edit placement rule') : t('New placement rule')}
  width={600}
  onclose={() => (showDialog = false)}
>
  <div class="dialog-content">
    {#if dialogError}
      <div class="ui-error-box">{dialogError}</div>
    {/if}

    <label class="ui-field">
      <span class="label">{t('Rule name')}</span>
      <input
        type="text"
        class="ui-input"
        bind:value={formName}
        placeholder={t('e.g. Separate Web Frontends')}
        required
      />
    </label>

    <label class="ui-field">
      <span class="label">{t('Constraint type')}</span>
      <select class="ui-input" bind:value={formKind}>
        <option value="vm_anti_affinity">{t('VM-VM Anti-Affinity (Separate Hosts)')}</option>
        <option value="vm_affinity">{t('VM-VM Affinity (Collocate on Same Host)')}</option>
        <option value="vm_host">{t('VM-Host Group (Pin to or Exclude from Hosts)')}</option>
      </select>
    </label>

    <div class="ui-field">
      <span class="label">{t('Rule strength')}</span>
      <div class="strength-options" role="radiogroup">
        <label class="radio-card" class:active={formStrength === 'must'}>
          <input type="radio" name="strength" value="must" bind:group={formStrength} />
          <div class="radio-body">
            <strong>{t('Must (Hard constraint)')}</strong>
            <span class="hint">{t('Strict requirement: workloads cannot violate this constraint under any circumstances.')}</span>
          </div>
        </label>
        <label class="radio-card" class:active={formStrength === 'should'}>
          <input type="radio" name="strength" value="should" bind:group={formStrength} />
          <div class="radio-body">
            <strong>{t('Should (Soft preference)')}</strong>
            <span class="hint">{t('Preference: honored during placement and DRS balancing when feasible.')}</span>
          </div>
        </label>
      </div>
    </div>

    <!-- VM Multi-Select -->
    <div class="ui-field">
      <div class="field-head">
        <span class="label">{t('Virtual machines')}</span>
        <span class="selected-count">{t('{n} virtual machine(s) selected', { n: formDomains.length })}</span>
      </div>
      <input
        type="search"
        class="ui-input sm search-vm"
        placeholder={t('Filter VMs…')}
        bind:value={vmFilter}
      />
      <div class="multi-select-box">
        {#if filteredInventory.length === 0}
          <p class="muted small pad">{t('No virtual machines match filter.')}</p>
        {:else}
          {#each filteredInventory as vm (vm.uuid)}
            <label class="check-item">
              <input
                type="checkbox"
                checked={formDomains.includes(vm.uuid)}
                onchange={() => toggleDomain(vm.uuid)}
              />
              <span class="item-name">{vm.name}</span>
              {#if vm.hostName}
                <span class="item-host">({vm.hostName})</span>
              {/if}
            </label>
          {/each}
        {/if}
      </div>
    </div>

    <!-- Host Multi-Select (for vm_host) -->
    {#if formKind === 'vm_host'}
      <div class="ui-field">
        <span class="label">{t('Target hosts')}</span>
        <div class="multi-select-box host-box">
          {#each cluster.members as m (m.id)}
            <label class="check-item">
              <input
                type="checkbox"
                checked={formHosts.includes(m.id)}
                onchange={() => toggleHost(m.id)}
              />
              <span class="item-name">{m.name}</span>
              <span class="item-sub mono">{m.address}</span>
            </label>
          {/each}
        </div>
        <label class="checkbox-line">
          <input type="checkbox" bind:checked={formExclude} />
          <span>{t('Exclude these hosts (must not / should not run on selected hosts)')}</span>
        </label>
      </div>
    {/if}

    <label class="checkbox-line">
      <input type="checkbox" bind:checked={formEnabled} />
      <span>{t('Rule enabled')}</span>
    </label>
  </div>

  {#snippet footer()}
    <button class="ui-btn ghost" type="button" onclick={() => (showDialog = false)}>
      {t('Cancel')}
    </button>
    <button class="ui-btn primary" type="button" disabled={savingRule} onclick={saveRule}>
      {savingRule ? t('Saving…') : editingId ? t('Keep changes') : t('Create rule')}
    </button>
  {/snippet}
</Dialog>

<style>
  .placement-deck {
    display: flex;
    flex-direction: column;
    gap: 16px;
  }
  .card-head {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 12px 16px;
    border-bottom: 1px solid var(--border);
    flex-wrap: wrap;
    gap: 8px;
  }
  .title-with-icon {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .title-with-icon h2 {
    margin: 0;
    font-size: 15px;
    font-weight: 700;
  }
  .subtitle {
    margin: 2px 0 0;
    font-size: 12px;
    color: var(--text-3);
  }
  :global(.viol-icon) {
    color: var(--warn);
  }
  .count-badge {
    padding: 1px 7px;
    background: var(--danger);
    color: var(--accent-contrast);
    font-size: 11px;
    font-weight: 800;
  }
  .empty-ok {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 16px;
    color: var(--ok);
  }
  .empty-ok strong {
    color: var(--text);
    font-size: 13.5px;
  }
  .empty-ok p {
    margin: 2px 0 0;
    font-size: 12px;
  }
  .empty-panel {
    padding: 24px 16px;
    text-align: center;
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 10px;
  }
  .violations-list {
    display: flex;
    flex-direction: column;
  }
  .violation-row {
    display: flex;
    align-items: flex-start;
    gap: 12px;
    padding: 10px 16px;
    border-bottom: 1px solid var(--border);
    background: var(--surface);
  }
  .violation-row:last-child {
    border-bottom: none;
  }
  .violation-row.hard {
    border-left: 3px solid var(--danger);
  }
  .sev-badge {
    padding: 2px 6px;
    font-size: 10px;
    font-weight: 800;
    letter-spacing: 0.05em;
    background: color-mix(in srgb, var(--warn) 18%, transparent);
    color: var(--warn);
    border: 1px solid var(--warn);
  }
  .sev-badge.hard {
    background: var(--danger-soft);
    color: var(--danger);
    border: 1px solid var(--danger);
  }
  .v-main {
    flex: 1;
    min-width: 0;
  }
  .v-top {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
    font-size: 13px;
  }
  .host-pill,
  .vm-pill {
    padding: 1px 6px;
    background: var(--surface-2);
    border: 1px solid var(--border);
    font-size: 11px;
    color: var(--text-2);
  }
  .v-msg {
    margin: 4px 0 0;
    font-size: 12px;
    color: var(--text-2);
  }
  .rules-list {
    display: flex;
    flex-direction: column;
  }
  .rule-row {
    display: flex;
    align-items: center;
    gap: 16px;
    padding: 12px 16px;
    border-bottom: 1px solid var(--border);
  }
  .rule-row:last-child {
    border-bottom: none;
  }
  .rule-row.disabled {
    opacity: 0.6;
  }
  .rule-meta {
    flex: 0 0 240px;
    display: flex;
    flex-direction: column;
    gap: 4px;
    min-width: 0;
  }
  .badges-row {
    display: flex;
    align-items: center;
    gap: 6px;
    flex-wrap: wrap;
  }
  .kind-tag {
    padding: 1px 6px;
    font-size: 10.5px;
    font-weight: 700;
    background: var(--surface-2);
    border: 1px solid var(--border);
    color: var(--text);
  }
  .kind-tag.anti {
    color: var(--danger);
    border-color: color-mix(in srgb, var(--danger) 40%, transparent);
  }
  .kind-tag.aff {
    color: var(--info);
    border-color: color-mix(in srgb, var(--info) 40%, transparent);
  }
  .strength-tag {
    padding: 1px 6px;
    font-size: 10.5px;
    font-weight: 700;
  }
  .strength-tag.solid {
    background: var(--accent);
    color: var(--accent-contrast);
  }
  .strength-tag.outlined {
    background: transparent;
    border: 1px solid var(--accent);
    color: var(--accent);
  }
  .disabled-pill {
    padding: 1px 5px;
    background: var(--surface-2);
    font-size: 10px;
    color: var(--text-3);
  }
  .rule-name {
    font-weight: 600;
    font-size: 13.5px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .rule-details {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 3px;
    font-size: 12.5px;
  }
  .vms-line,
  .hosts-line {
    display: flex;
    align-items: center;
    gap: 6px;
    flex-wrap: wrap;
  }
  .lbl {
    color: var(--text-3);
    font-size: 11.5px;
  }
  .vals {
    color: var(--text);
    font-weight: 500;
  }
  .pill-excluded {
    padding: 1px 6px;
    background: var(--danger-soft);
    color: var(--danger);
    border: 1px solid var(--danger);
    font-size: 10.5px;
    font-weight: 700;
  }
  .rule-actions {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .host-labels-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
    gap: 12px;
    padding: 14px 16px;
  }
  .host-label-box {
    display: flex;
    flex-direction: column;
    gap: 10px;
    padding: 12px;
    border: 1px solid var(--border);
    background: var(--surface);
  }
  .host-box-header {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 13.5px;
  }
  .pill {
    padding: 1px 6px;
    background: var(--surface-2);
    font-size: 11px;
    color: var(--text-2);
  }
  .labels-cloud {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    min-height: 24px;
    align-items: center;
  }
  .label-chip {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    padding: 2px 6px;
    border: 1px solid var(--border);
    background: var(--surface-2);
    font-size: 11.5px;
  }
  .label-chip .k {
    font-weight: 700;
    color: var(--text);
  }
  .label-chip .v {
    color: var(--text-2);
  }
  .del-lbl-btn {
    border: none;
    background: transparent;
    color: var(--text-3);
    cursor: pointer;
    padding: 0 2px;
    font-size: 12px;
  }
  .del-lbl-btn:hover {
    color: var(--danger);
  }
  .add-label-row {
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .add-label-row input {
    flex: 1;
    min-width: 0;
  }
  .eq {
    color: var(--text-3);
    font-weight: 700;
  }
  .save-row {
    display: flex;
    justify-content: flex-end;
  }
  .dialog-content {
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .strength-options {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 10px;
    margin-top: 4px;
  }
  .radio-card {
    display: flex;
    align-items: flex-start;
    gap: 8px;
    padding: 10px;
    border: 1px solid var(--border);
    background: var(--surface);
    cursor: pointer;
  }
  .radio-card.active {
    border-color: var(--accent);
    background: var(--accent-soft);
  }
  .radio-card input {
    margin-top: 3px;
  }
  .radio-body {
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .radio-body strong {
    font-size: 12.5px;
  }
  .hint {
    font-size: 11px;
    color: var(--text-3);
    line-height: 1.3;
  }
  .field-head {
    display: flex;
    justify-content: space-between;
    align-items: center;
  }
  .selected-count {
    font-size: 11.5px;
    color: var(--accent);
    font-weight: 600;
  }
  .search-vm {
    margin-top: 4px;
    margin-bottom: 6px;
  }
  .multi-select-box {
    max-height: 160px;
    overflow-y: auto;
    border: 1px solid var(--border);
    background: var(--surface-2);
    display: flex;
    flex-direction: column;
  }
  .host-box {
    max-height: 120px;
    margin-top: 4px;
  }
  .check-item {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 6px 10px;
    border-bottom: 1px solid var(--border);
    font-size: 12.5px;
    cursor: pointer;
  }
  .check-item:last-child {
    border-bottom: none;
  }
  .check-item:hover {
    background: var(--surface-hover);
  }
  .item-name {
    font-weight: 600;
  }
  .item-host {
    font-size: 11px;
    color: var(--text-3);
  }
  .item-sub {
    font-size: 11px;
    color: var(--text-3);
    margin-left: auto;
  }
  .checkbox-line {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 12.5px;
    cursor: pointer;
    margin-top: 6px;
  }
  .pad {
    padding: 10px;
    margin: 0;
  }
  .muted {
    color: var(--text-3);
  }
  .small {
    font-size: 11px;
  }
  @media (max-width: 760px) {
    .rule-row {
      flex-direction: column;
      align-items: flex-start;
      gap: 10px;
    }
    .rule-meta {
      flex: none;
      width: 100%;
    }
    .rule-actions {
      width: 100%;
      justify-content: flex-end;
    }
    .strength-options {
      grid-template-columns: 1fr;
    }
  }
</style>
