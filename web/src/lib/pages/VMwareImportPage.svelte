<script lang="ts">
  import { onMount } from 'svelte'
  import { t } from '../i18n.svelte'
  import {
    api,
    errorText,
    type Job,
    type Pool,
    type VNetwork,
    type VMwareConnection,
    type VMwareInventory,
    type VMwarePreflightResult,
    type VMwareVM,
    type VMwareWave,
  } from '../api'
  import { activePools } from '../media'
  import { machineHref, router } from '../router.svelte'
  import { session } from '../session.svelte'
  import { live } from '../live.svelte'
  import { toasts } from '../toast.svelte'
  import { formatBytes, formatKiB } from '../format'
  import {
    aggregateWaveStates,
    distinctPortGroups,
    filterVMs,
    groupVMsByTree,
    hasHardErrors,
    isPreflightWarning,
  } from '../vmware'

  // Lucide icons
  import ArrowLeft from '@lucide/svelte/icons/arrow-left'
  import Server from '@lucide/svelte/icons/server'
  import Check from '@lucide/svelte/icons/check'
  import CircleCheck from '@lucide/svelte/icons/circle-check'
  import CircleAlert from '@lucide/svelte/icons/circle-alert'
  import TriangleAlert from '@lucide/svelte/icons/triangle-alert'
  import RotateCw from '@lucide/svelte/icons/rotate-cw'
  import ExternalLink from '@lucide/svelte/icons/external-link'
  import Search from '@lucide/svelte/icons/search'
  import HardDrive from '@lucide/svelte/icons/hard-drive'
  import Network from '@lucide/svelte/icons/network'
  import Cpu from '@lucide/svelte/icons/cpu'
  import Folder from '@lucide/svelte/icons/folder'

  type WizardStep = 1 | 2 | 3 | 4
  let step = $state<WizardStep>(1)
  const admin = $derived(session.can('admin'))

  // ---- Step 1: Connection -----------------------------------------------------------------
  let url = $state('')
  let username = $state('')
  let password = $state('')
  let thumbprint = $state('')
  let insecure = $state(false)
  let esxi = $state(false)

  let savedConn = $state<VMwareConnection | null>(null)
  let isEditingConn = $state(false)
  let isTesting = $state(false)
  let isSaving = $state(false)
  let testResult = $state<{ ok: boolean; message: string } | null>(null)
  let connError = $state('')

  const isConfigured = $derived(Boolean(url.trim() && (password || savedConn?.password)))

  async function loadConnection() {
    try {
      const c = await api.vmwareConnection()
      if (c && c.url) {
        savedConn = c
        url = c.url || ''
        username = c.username || ''
        password = c.password || ''
        thumbprint = c.thumbprint || ''
        insecure = Boolean(c.insecure)
        esxi = Boolean(c.esxi)
        isEditingConn = false
      } else {
        isEditingConn = true
      }
    } catch {
      isEditingConn = true
    }
  }

  async function testConnection() {
    isTesting = true
    testResult = null
    connError = ''
    try {
      const res = await api.vmwareTest({
        url: url.trim(),
        username: username.trim(),
        password: password || undefined,
        thumbprint: thumbprint.trim() || undefined,
        insecure,
        esxi,
      })
      testResult = res
      if (res.ok) {
        toasts.push({ kind: 'info', title: t('Connection verified'), detail: res.message })
      } else {
        toasts.push({ kind: 'error', title: t('Connection test failed'), detail: res.message })
      }
    } catch (e) {
      testResult = { ok: false, message: errorText(e) }
      toasts.push({ kind: 'error', title: t('Connection test failed'), detail: errorText(e) })
    } finally {
      isTesting = false
    }
  }

  async function saveConnection() {
    if (!url.trim()) return
    isSaving = true
    connError = ''
    try {
      const saved = await api.vmwareSaveConnection({
        url: url.trim(),
        username: username.trim(),
        password: password || undefined,
        thumbprint: thumbprint.trim() || undefined,
        insecure,
        esxi,
      })
      savedConn = saved
      password = saved.password || '••••••••'
      isEditingConn = false
      toasts.push({ kind: 'info', title: t('Connection saved') })
    } catch (e) {
      connError = errorText(e)
    } finally {
      isSaving = false
    }
  }

  // ---- Step 2: Inventory ------------------------------------------------------------------
  let inventory = $state<VMwareInventory | null>(null)
  let loadingInventory = $state(false)
  let inventoryError = $state('')
  let searchQuery = $state('')
  let selectedVMIds = $state<string[]>([])

  async function loadInventory() {
    loadingInventory = true
    inventoryError = ''
    try {
      inventory = await api.vmwareInventory()
    } catch (e) {
      inventoryError = errorText(e)
    } finally {
      loadingInventory = false
    }
  }

  const allVMs = $derived(inventory?.vms ?? [])
  const filteredVMs = $derived(filterVMs(allVMs, searchQuery))
  const treeGroups = $derived(groupVMsByTree(filteredVMs))

  const eligibleFilteredVMs = $derived(filteredVMs.filter((v) => v.powerState === 'off'))
  const allFilteredSelected = $derived(
    eligibleFilteredVMs.length > 0 && eligibleFilteredVMs.every((v) => selectedVMIds.includes(v.id)),
  )

  function toggleSelectAllFiltered() {
    if (allFilteredSelected) {
      const idsToRemove = new Set(eligibleFilteredVMs.map((v) => v.id))
      selectedVMIds = selectedVMIds.filter((id) => !idsToRemove.has(id))
    } else {
      const current = new Set(selectedVMIds)
      for (const v of eligibleFilteredVMs) {
        current.add(v.id)
      }
      selectedVMIds = Array.from(current)
    }
  }

  function toggleVMSelection(vm: VMwareVM) {
    if (vm.powerState !== 'off') return
    if (selectedVMIds.includes(vm.id)) {
      selectedVMIds = selectedVMIds.filter((id) => id !== vm.id)
    } else {
      selectedVMIds = [...selectedVMIds, vm.id]
    }
  }

  // ---- Step 3: Target & Preflight ---------------------------------------------------------
  let pools = $state<Pool[]>([])
  let pool = $state('')
  let virtoNetworks = $state<VNetwork[]>([])
  let networkMap = $state<Record<string, string>>({})
  let virtioIso = $state('')
  let parallel = $state(2)
  let singleTargetName = $state('')

  let preflightLoading = $state(false)
  let preflightResult = $state<VMwarePreflightResult | null>(null)
  let preflightError = $state('')

  const selectedVMs = $derived(allVMs.filter((v) => selectedVMIds.includes(v.id)))
  const neededPortGroups = $derived(distinctPortGroups(selectedVMs))
  const hasHardPreflightErrors = $derived(preflightResult ? hasHardErrors(preflightResult.items) : false)

  async function loadTargets() {
    try {
      const [p, n] = await Promise.all([activePools(), api.networks()])
      pools = p.filter((x) => x.type === 'dir' || x.type === 'netfs')
      if (!pool && pools.length > 0) {
        pool = (pools.find((x) => x.name === 'default') ?? pools[0]).name
      }
      virtoNetworks = n
      // Auto-assign default network mapping for distinct port groups
      const defaultNet = n.find((x) => x.name === 'default')?.name ?? n[0]?.name ?? ''
      const updatedMap: Record<string, string> = { ...networkMap }
      for (const pg of neededPortGroups) {
        if (!updatedMap[pg]) {
          updatedMap[pg] = defaultNet
        }
      }
      networkMap = updatedMap
    } catch {
      // Handled gracefully in UI
    }
  }

  async function runPreflight() {
    if (!pool || selectedVMIds.length === 0) return
    preflightLoading = true
    preflightError = ''
    try {
      preflightResult = await api.vmwarePreflight({
        vmIds: selectedVMIds,
        pool,
      })
    } catch (e) {
      preflightError = errorText(e)
    } finally {
      preflightLoading = false
    }
  }

  // ---- Step 4: Run & Live Progress --------------------------------------------------------
  let currentJob = $state<Job | null>(null)
  let currentWave = $state<VMwareWave | null>(null)
  let waveId = $state('')
  let pollTimer: ReturnType<typeof setInterval> | null = null
  let isRetrying = $state(false)

  const isSingle = $derived(selectedVMs.length === 1)
  const waveStats = $derived(
    currentWave
      ? aggregateWaveStates(currentWave.items)
      : {
          total: selectedVMs.length,
          pending: currentJob?.state === 'running' ? 0 : selectedVMs.length,
          running: currentJob?.state === 'running' ? selectedVMs.length : 0,
          done: currentJob?.state === 'succeeded' ? selectedVMs.length : 0,
          failed: currentJob?.state === 'failed' ? selectedVMs.length : 0,
          finished: currentJob?.state === 'succeeded' || currentJob?.state === 'failed',
          allDone: currentJob?.state === 'succeeded',
          hasFailures: currentJob?.state === 'failed',
        },
  )

  function stopPolling() {
    if (pollTimer) {
      clearInterval(pollTimer)
      pollTimer = null
    }
  }

  function startPolling(jobId: string, waveIdentifier?: string) {
    stopPolling()
    pollTimer = setInterval(async () => {
      try {
        const j = await api.job(jobId)
        currentJob = j

        if (waveIdentifier) {
          const w = await api.vmwareWave(waveIdentifier).catch(() => null)
          if (w) currentWave = w
        } else {
          // Check if a wave ID was returned in job result
          if (j.result && !currentWave) {
            const w = await api.vmwareWave(j.result).catch(() => null)
            if (w) {
              currentWave = w
              waveId = w.id
            }
          }
        }

        if (j.state === 'succeeded' || j.state === 'failed') {
          stopPolling()
          if (j.state === 'succeeded') {
            toasts.push({ kind: 'info', title: t('Migration completed') })
          } else {
            toasts.push({ kind: 'error', title: t('Migration finished with failures'), detail: j.message })
          }
        }
      } catch {
        // Retry next tick
      }
    }, 1500)
  }

  async function startMigration() {
    if (selectedVMs.length === 0 || !pool) return
    step = 4
    currentJob = null
    currentWave = null
    waveId = ''
    stopPolling()

    try {
      if (isSingle) {
        const targetVM = selectedVMs[0]
        const nameToUse = singleTargetName.trim() || targetVM.name
        const job = await api.vmwareImport({
          vmId: targetVM.id,
          pool,
          name: nameToUse,
          networkMap,
          virtioIso: virtioIso.trim() || undefined,
        })
        currentJob = job
        startPolling(job.id)
      } else {
        const job = await api.vmwareCreateWave({
          vmIds: selectedVMIds,
          pool,
          parallel,
          networkMap,
          virtioIso: virtioIso.trim() || undefined,
        })
        currentJob = job
        // Query wave registry to link current wave
        const waves = await api.vmwareWaves().catch(() => [])
        if (waves.length > 0) {
          currentWave = waves[0]
          waveId = waves[0].id
        }
        startPolling(job.id, waveId)
      }
    } catch (e) {
      toasts.push({ kind: 'error', title: t('Failed to start migration'), detail: errorText(e) })
    }
  }

  async function retryFailedWave() {
    if (!waveId && !currentWave?.id) return
    const id = waveId || currentWave!.id
    isRetrying = true
    try {
      const job = await api.vmwareRetry(id, {
        pool,
        networkMap,
        virtioIso: virtioIso.trim() || undefined,
      })
      currentJob = job
      toasts.push({ kind: 'info', title: t('Retry started'), detail: t('Retrying failed machines…') })
      startPolling(job.id, id)
    } catch (e) {
      toasts.push({ kind: 'error', title: t('Retry failed'), detail: errorText(e) })
    } finally {
      isRetrying = false
    }
  }

  function machineUuidFor(name: string): string | null {
    if (isSingle && currentJob?.state === 'succeeded' && currentJob.result) {
      return currentJob.result
    }
    const found = (live.snapshot?.domains ?? []).find((d) => d.name === name)
    return found?.uuid ?? null
  }

  function resetWizard() {
    stopPolling()
    selectedVMIds = []
    currentJob = null
    currentWave = null
    waveId = ''
    step = 2
  }

  // Step transitions
  async function goToStep(s: WizardStep) {
    if (s === 2) {
      if (!isConfigured) return
      step = 2
      if (!inventory) void loadInventory()
    } else if (s === 3) {
      if (selectedVMIds.length === 0) return
      step = 3
      if (selectedVMs.length === 1 && !singleTargetName) {
        singleTargetName = selectedVMs[0].name
      }
      await loadTargets()
      void runPreflight()
    } else if (s === 4) {
      void startMigration()
    } else {
      step = s
    }
  }

  onMount(() => {
    void loadConnection()
    return () => stopPolling()
  })
</script>

<svelte:head><title>{t('VMware Migration · VirtoMir')}</title></svelte:head>

<header class="page-head">
  <div>
    <a href={router.scoped('/machines/import')} onclick={router.link} class="back-link">
      <ArrowLeft size={16} /> {t('Back to Import')}
    </a>
    <h1>{t('VMware vCenter / ESXi Migration')}</h1>
    <p class="lead">{t('Migrate virtual machines directly from VMware into VirtoMir.')}</p>
  </div>
</header>

{#if !admin}
  <div class="ui-error-box">{session.why('admin')}</div>
{/if}

<!-- Wizard Stepper Navigation -->
<nav class="wizard-nav" aria-label={t('Migration Steps')}>
  <button
    class="step-pill"
    class:active={step === 1}
    class:completed={step > 1 && isConfigured}
    onclick={() => (step > 1 ? (step = 1) : null)}
  >
    <span class="num">{step > 1 && isConfigured ? '✓' : '1'}</span>
    <span class="label">{t('Connection')}</span>
  </button>
  <button
    class="step-pill"
    class:active={step === 2}
    class:completed={step > 2}
    disabled={!isConfigured}
    onclick={() => (step > 2 ? (step = 2) : null)}
  >
    <span class="num">{step > 2 ? '✓' : '2'}</span>
    <span class="label">{t('Inventory')}</span>
  </button>
  <button
    class="step-pill"
    class:active={step === 3}
    class:completed={step > 3}
    disabled={selectedVMIds.length === 0}
    onclick={() => (step > 3 ? (step = 3) : null)}
  >
    <span class="num">{step > 3 ? '✓' : '3'}</span>
    <span class="label">{t('Target & Options')}</span>
  </button>
  <button
    class="step-pill"
    class:active={step === 4}
    disabled={step < 4}
  >
    <span class="num">4</span>
    <span class="label">{t('Run & Progress')}</span>
  </button>
</nav>

<div class="wizard-container">
  <!-- ==================== STEP 1: CONNECTION ==================== -->
  {#if step === 1}
    <section class="ui-panel card step-card">
      <div class="card-header">
        <h2><Server size={18} /> {t('VMware vCenter or ESXi Connection')}</h2>
        {#if savedConn?.url && !isEditingConn}
          <span class="ui-badge ok"><Check size={13} /> {t('Configured')}</span>
        {/if}
      </div>
      <p class="hint">
        {t('VirtoMir uses standard vSphere SOAP APIs to read inventory and virt-v2v to stream disks directly into your storage pool.')}
      </p>

      {#if savedConn?.url && !isEditingConn}
        <!-- Masked Saved Connection Display -->
        <div class="saved-box">
          <div class="saved-details">
            <div class="detail-row">
              <span class="label">{t('Address (URL)')}:</span>
              <span class="val mono">{savedConn.url}</span>
            </div>
            <div class="detail-row">
              <span class="label">{t('Username')}:</span>
              <span class="val mono">{savedConn.username || '—'}</span>
            </div>
            <div class="detail-row">
              <span class="label">{t('Password')}:</span>
              <span class="val mono">{savedConn.password || '••••••••'}</span>
            </div>
            {#if savedConn.thumbprint}
              <div class="detail-row">
                <span class="label">{t('TLS Thumbprint')}:</span>
                <span class="val mono">{savedConn.thumbprint}</span>
              </div>
            {/if}
            {#if savedConn.esxi}
              <div class="detail-row">
                <span class="label">{t('Type')}:</span>
                <span class="val">{t('Standalone ESXi host')}</span>
              </div>
            {/if}
          </div>
          <div class="saved-actions">
            <button class="ui-btn sm ghost" onclick={() => (isEditingConn = true)}>
              {t('Edit connection')}
            </button>
            <button class="ui-btn sm" disabled={isTesting} onclick={testConnection}>
              {#if isTesting}<RotateCw class="spin" size={13} />{:else}<Check size={13} />{/if}
              {t('Test connection')}
            </button>
          </div>
        </div>
      {:else}
        <!-- Connection Edit Form -->
        <form class="form" onsubmit={(e) => { e.preventDefault(); void saveConnection(); }}>
          <div class="row2">
            <label class="ui-field">
              <span class="label">{t('vCenter or ESXi Address')}</span>
              <input
                class="ui-input"
                bind:value={url}
                placeholder="https://vcenter.example.com"
                required
              />
              <span class="hint">{t('HTTPS URL without user:password or trailing path.')}</span>
            </label>
            <label class="ui-field">
              <span class="label">{t('Username')}</span>
              <input
                class="ui-input"
                bind:value={username}
                placeholder="administrator@vsphere.local"
                required
              />
              <span class="hint">{t('Account with Administrator or Read-Only + Export permissions.')}</span>
            </label>
          </div>

          <div class="row2">
            <label class="ui-field">
              <span class="label">{t('Password')}</span>
              <input
                class="ui-input"
                type="password"
                bind:value={password}
                placeholder={savedConn?.password || t('Enter password')}
              />
              <span class="hint">{t('Stored securely with 0600 on this host, never reaches logs.')}</span>
            </label>
            <label class="ui-field">
              <span class="label">{t('TLS Thumbprint')} <span class="opt">{t('(optional)')}</span></span>
              <input
                class="ui-input mono"
                bind:value={thumbprint}
                placeholder="11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF:00:11:22:33:44"
              />
              <span class="hint">{t('SHA-1 or SHA-256 fingerprint in hex to pin the host certificate.')}</span>
            </label>
          </div>

          <div class="checkbox-row">
            <label class="checkbox-label">
              <input type="checkbox" bind:checked={insecure} />
              <span>{t('Accept self-signed certificate (skip CA verification)')}</span>
            </label>
            <label class="checkbox-label">
              <input type="checkbox" bind:checked={esxi} />
              <span>{t('Standalone ESXi host (direct connection without vCenter)')}</span>
            </label>
          </div>

          {#if connError}
            <div class="ui-error-box">{connError}</div>
          {/if}

          <div class="actions">
            {#if savedConn?.url}
              <button type="button" class="ui-btn ghost" onclick={() => (isEditingConn = false)}>
                {t('Cancel')}
              </button>
            {/if}
            <button
              type="button"
              class="ui-btn"
              disabled={!url || isTesting}
              onclick={testConnection}
            >
              {#if isTesting}<RotateCw class="spin" size={14} />{:else}<Check size={14} />{/if}
              {t('Test connection')}
            </button>
            <button
              type="submit"
              class="ui-btn primary"
              disabled={!url || isSaving}
            >
              {#if isSaving}<RotateCw class="spin" size={14} />{/if}
              {t('Save connection')}
            </button>
          </div>
        </form>
      {/if}

      {#if testResult}
        <div class="test-banner" class:ok={testResult.ok} class:err={!testResult.ok}>
          {#if testResult.ok}
            <CircleCheck size={16} />
          {:else}
            <CircleAlert size={16} />
          {/if}
          <span>{testResult.message}</span>
        </div>
      {/if}

      <div class="step-footer">
        <div></div>
        <button
          class="ui-btn primary"
          disabled={!isConfigured || !admin}
          onclick={() => void goToStep(2)}
        >
          {t('Continue to Inventory')} →
        </button>
      </div>
    </section>

  <!-- ==================== STEP 2: INVENTORY ==================== -->
  {:else if step === 2}
    <section class="ui-panel card step-card">
      <div class="card-header">
        <h2>{t('VMware Virtual Machines')}</h2>
        <div class="header-tools">
          <button class="ui-btn sm ghost" disabled={loadingInventory} onclick={loadInventory}>
            <RotateCw class={loadingInventory ? 'spin' : ''} size={14} /> {t('Refresh')}
          </button>
        </div>
      </div>
      <p class="hint">
        {t('Select powered-off virtual machines to migrate. Running machines must be powered off in VMware before migration.')}
      </p>

      <!-- Search & Bulk Selection Toolbar -->
      <div class="inventory-toolbar">
        <div class="search-box">
          <Search size={15} class="search-icon" />
          <input
            class="ui-input search-input"
            type="search"
            bind:value={searchQuery}
            placeholder={t('Filter by VM name, guest OS, host, or datacenter…')}
          />
        </div>
        <div class="selection-indicator">
          <label class="select-all-label">
            <input
              type="checkbox"
              checked={allFilteredSelected}
              disabled={eligibleFilteredVMs.length === 0}
              onchange={toggleSelectAllFiltered}
            />
            <span>{t('Select all eligible filtered')}</span>
          </label>
          <span class="count-pill">
            {t('{selected} of {total} selected', { selected: selectedVMIds.length, total: allVMs.filter((v) => v.powerState === 'off').length })}
          </span>
        </div>
      </div>

      {#if loadingInventory}
        <div class="loading-state">
          <RotateCw class="spin" size={24} />
          <p>{t('Reading VMware inventory…')}</p>
        </div>
      {:else if inventoryError}
        <div class="ui-error-box">
          <p>{inventoryError}</p>
          <button class="ui-btn sm mt-2" onclick={loadInventory}>{t('Try again')}</button>
        </div>
      {:else if allVMs.length === 0}
        <div class="empty-state">
          <p>{t('No virtual machines found in this VMware environment.')}</p>
        </div>
      {:else if filteredVMs.length === 0}
        <div class="empty-state">
          <p>{t('No machines match "{query}".', { query: searchQuery })}</p>
        </div>
      {:else}
        <!-- Tree View: Datacenter → Host → VM -->
        <div class="inventory-tree">
          {#each treeGroups as dc (dc.name)}
            <div class="tree-dc">
              <div class="dc-header">
                <Folder size={15} />
                <span class="dc-title">{dc.name}</span>
                <span class="dc-count">({dc.hosts.reduce((acc, h) => acc + h.vms.length, 0)} {t('VMs')})</span>
              </div>

              {#each dc.hosts as hostGroup (hostGroup.name)}
                <div class="tree-host">
                  <div class="host-header">
                    <Server size={14} />
                    <span class="host-title">{hostGroup.name}</span>
                  </div>

                  <table class="vm-table">
                    <thead>
                      <tr>
                        <th class="w-check"></th>
                        <th>{t('Machine')}</th>
                        <th>{t('State')}</th>
                        <th>{t('vCPUs & Memory')}</th>
                        <th>{t('Disks')}</th>
                        <th>{t('Guest OS')}</th>
                      </tr>
                    </thead>
                    <tbody>
                      {#each hostGroup.vms as vm (vm.id)}
                        {@const isOff = vm.powerState === 'off'}
                        {@const isChecked = selectedVMIds.includes(vm.id)}
                        <tr
                          class="vm-row"
                          class:selected={isChecked}
                          class:disabled={!isOff}
                          onclick={() => isOff && toggleVMSelection(vm)}
                        >
                          <td class="w-check" onclick={(e) => e.stopPropagation()}>
                            <input
                              type="checkbox"
                              checked={isChecked}
                              disabled={!isOff}
                              onchange={() => toggleVMSelection(vm)}
                              aria-label={t('Select {name}', { name: vm.name })}
                            />
                          </td>
                          <td class="vm-name-cell">
                            <strong>{vm.name}</strong>
                          </td>
                          <td>
                            {#if vm.powerState === 'off'}
                              <span class="state-pill off">{t('Powered off')}</span>
                            {:else if vm.powerState === 'on'}
                              <span class="state-pill on" title={t('Shut down in VMware first')}>
                                {t('Powered on')}
                              </span>
                              <span class="state-hint">{t('Shut down in VMware first')}</span>
                            {:else}
                              <span class="state-pill warn">{vm.powerState}</span>
                              <span class="state-hint">{t('Shut down in VMware first')}</span>
                            {/if}
                          </td>
                          <td class="muted">
                            {vm.vcpus} vCPUs · {formatKiB(vm.memoryMiB * 1024)}
                          </td>
                          <td class="muted">
                            {vm.disks.length} {vm.disks.length === 1 ? t('disk') : t('disks')}
                            ({formatBytes(vm.disks.reduce((acc, d) => acc + d.capacityBytes, 0))})
                          </td>
                          <td class="muted">
                            {vm.guestOs || t('Other')}
                          </td>
                        </tr>
                      {/each}
                    </tbody>
                  </table>
                </div>
              {/each}
            </div>
          {/each}
        </div>
      {/if}

      <div class="step-footer">
        <button class="ui-btn ghost" onclick={() => (step = 1)}>
          ← {t('Back')}
        </button>
        <button
          class="ui-btn primary"
          disabled={selectedVMIds.length === 0}
          onclick={() => void goToStep(3)}
        >
          {t('Continue to Target & Options')} ({selectedVMIds.length}) →
        </button>
      </div>
    </section>

  <!-- ==================== STEP 3: TARGET & PREFLIGHT ==================== -->
  {:else if step === 3}
    <section class="ui-panel card step-card">
      <div class="card-header">
        <h2>{t('Destination & Conversion Settings')}</h2>
        <span class="count-pill">
          {t('{count} machines selected', { count: selectedVMs.length })}
        </span>
      </div>

      <div class="settings-grid">
        <!-- Single machine rename option -->
        {#if isSingle}
          <label class="ui-field">
            <span class="label">{t('Machine Name in VirtoMir')}</span>
            <input
              class="ui-input"
              bind:value={singleTargetName}
              placeholder={selectedVMs[0]?.name}
            />
            <span class="hint">{t('Name the virtual machine will have in VirtoMir.')}</span>
          </label>
        {/if}

        <!-- Storage Pool Picker -->
        <label class="ui-field">
          <span class="label">{t('Target Storage Pool')}</span>
          <select class="ui-input" bind:value={pool} onchange={() => void runPreflight()}>
            {#each pools as p (p.name)}
              <option value={p.name}>
                {p.name} ({formatBytes(p.availableBytes)} {t('available')})
              </option>
            {/each}
          </select>
          <span class="hint">{t('Directory storage pool where converted disk images (.qcow2) are placed.')}</span>
        </label>

        <!-- Concurrency (Parallelism) -->
        <label class="ui-field">
          <span class="label">{t('Migration Parallelism (1–4)')}</span>
          <select class="ui-input" bind:value={parallel}>
            <option value={1}>1 {t('concurrent conversion')}</option>
            <option value={2}>2 {t('concurrent conversions (recommended)')}</option>
            <option value={3}>3 {t('concurrent conversions')}</option>
            <option value={4}>4 {t('concurrent conversions (high host I/O)')}</option>
          </select>
          <span class="hint">{t('How many virtual machines will be converted simultaneously.')}</span>
        </label>

        <!-- Optional VirtIO Driver ISO -->
        <label class="ui-field full-width">
          <span class="label">{t('Windows VirtIO Drivers ISO Path')} <span class="opt">{t('(optional)')}</span></span>
          <input
            class="ui-input mono"
            bind:value={virtioIso}
            placeholder="/var/lib/libvirt/images/virtio-win.iso"
          />
          <span class="hint">
            {t('Optional path on this host to virtio-win.iso. If left empty, virt-v2v injects drivers it detects on the host.')}
          </span>
        </label>
      </div>

      <!-- Network Mapping Table -->
      <div class="network-mapping-section">
        <h3><Network size={16} /> {t('Network Port Group Mapping')}</h3>
        <p class="hint">
          {t('Map each VMware port group to a target VirtoMir network interface.')}
        </p>

        {#if neededPortGroups.length === 0}
          <p class="empty-hint">{t('Selected machines have no network adapters.')}</p>
        {:else}
          <table class="mapping-table">
            <thead>
              <tr>
                <th>{t('VMware Port Group')}</th>
                <th class="w-arrow"></th>
                <th>{t('VirtoMir Target Network')}</th>
              </tr>
            </thead>
            <tbody>
              {#each neededPortGroups as pg (pg)}
                <tr>
                  <td class="mono font-bold">{pg}</td>
                  <td class="w-arrow">→</td>
                  <td>
                    <select class="ui-input" bind:value={networkMap[pg]}>
                      {#each virtoNetworks as net (net.name)}
                        <option value={net.name}>
                          {net.name} ({net.mode}{net.bridge ? ` · ${net.bridge}` : ''})
                        </option>
                      {/each}
                    </select>
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        {/if}
      </div>

      <!-- Preflight Check Section -->
      <div class="preflight-section">
        <div class="preflight-header">
          <h3>{t('Preflight Health Check')}</h3>
          <button
            class="ui-btn sm ghost"
            disabled={preflightLoading}
            onclick={() => void runPreflight()}
          >
            <RotateCw class={preflightLoading ? 'spin' : ''} size={13} /> {t('Re-check')}
          </button>
        </div>

        {#if preflightLoading}
          <div class="preflight-box loading">
            <RotateCw class="spin" size={16} />
            <span>{t('Running preflight validation on selected machines…')}</span>
          </div>
        {:else if preflightError}
          <div class="ui-error-box">{preflightError}</div>
        {:else if preflightResult}
          <div class="preflight-summary">
            {#if hasHardPreflightErrors}
              <div class="status-banner err">
                <CircleAlert size={16} />
                <span>{t('Some preflight checks failed. Hard errors must be resolved before migration.')}</span>
              </div>
            {:else}
              <div class="status-banner ok">
                <CircleCheck size={16} />
                <span>{t('All preflight checks passed. Storage and conversion requirements are satisfied.')}</span>
              </div>
            {/if}

            <div class="preflight-items">
              {#each selectedVMs as vm (vm.id)}
                {@const issues = preflightResult.issues[vm.id] ?? []}
                {@const errors = issues.filter((iss) => !isPreflightWarning(iss))}
                {@const warnings = issues.filter((iss) => isPreflightWarning(iss))}
                <div class="vm-preflight-card" class:has-err={errors.length > 0} class:has-warn={warnings.length > 0}>
                  <div class="vm-pf-head">
                    <strong>{vm.name}</strong>
                    {#if errors.length > 0}
                      <span class="ui-badge danger">{t('Error')}</span>
                    {:else if warnings.length > 0}
                      <span class="ui-badge warn">{t('Warning')}</span>
                    {:else}
                      <span class="ui-badge ok"><Check size={12} /> {t('Ready')}</span>
                    {/if}
                  </div>
                  {#if errors.length > 0}
                    <ul class="issue-list err">
                      {#each errors as err}<li>{err}</li>{/each}
                    </ul>
                  {/if}
                  {#if warnings.length > 0}
                    <ul class="issue-list warn">
                      {#each warnings as warn}<li>{warn}</li>{/each}
                    </ul>
                  {/if}
                </div>
              {/each}
            </div>
          </div>
        {/if}
      </div>

      <div class="step-footer">
        <button class="ui-btn ghost" onclick={() => (step = 2)}>
          ← {t('Back')}
        </button>
        <button
          class="ui-btn primary"
          disabled={preflightLoading || hasHardPreflightErrors || !pool || !admin}
          onclick={() => void goToStep(4)}
        >
          {t('Start Migration')} ({selectedVMs.length}) →
        </button>
      </div>
    </section>

  <!-- ==================== STEP 4: RUN & LIVE PROGRESS ==================== -->
  {:else if step === 4}
    <section class="ui-panel card step-card">
      <div class="card-header">
        <h2>{t('Migration Wave Progress')}</h2>
        {#if waveStats.finished}
          {#if waveStats.hasFailures}
            <span class="ui-badge danger">{t('Finished with failures')}</span>
          {:else}
            <span class="ui-badge ok"><Check size={13} /> {t('Complete')}</span>
          {/if}
        {:else}
          <span class="ui-badge primary">
            <RotateCw class="spin" size={13} /> {t('In Progress')}
          </span>
        {/if}
      </div>

      <!-- Progress bar -->
      <div class="overall-progress-bar">
        <div
          class="bar-fill"
          style:width="{waveStats.total > 0 ? (waveStats.done / waveStats.total) * 100 : 0}%"
        ></div>
      </div>

      <div class="progress-summary-row">
        <span class="lead-text">
          {t('{done} of {total} machines converted', { done: waveStats.done, total: waveStats.total })}
        </span>
        {#if currentJob?.message}
          <span class="job-status-msg">{currentJob.message}</span>
        {/if}
      </div>

      <!-- List of VMs with Individual Progress -->
      <div class="wave-items-list">
        {#if currentWave?.items && currentWave.items.length > 0}
          {#each currentWave.items as item (item.vmId)}
            {@const domUuid = machineUuidFor(item.name)}
            <div class="wave-item-card" class:done={item.state === 'done'} class:failed={item.state === 'failed'}>
              <div class="item-main">
                <span class="item-name">{item.name}</span>
                {#if item.state === 'pending'}
                  <span class="state-pill muted">{t('Pending')}</span>
                {:else if item.state === 'running'}
                  <span class="state-pill on"><RotateCw class="spin" size={12} /> {t('Converting…')}</span>
                {:else if item.state === 'done'}
                  <span class="state-pill ok"><Check size={12} /> {t('Done')}</span>
                {:else if item.state === 'failed'}
                  <span class="state-pill danger">{t('Failed')}</span>
                {/if}
              </div>

              {#if item.message}
                <div class="item-msg" class:is-err={item.state === 'failed'}>{item.message}</div>
              {/if}

              {#if item.state === 'done'}
                <div class="item-link">
                  {#if domUuid}
                    <a
                      class="ui-btn sm ghost"
                      href={machineHref(domUuid)}
                      onclick={router.link}
                    >
                      <ExternalLink size={13} /> {t('Open machine')}
                    </a>
                  {:else}
                    <span class="hint">{t('Imported to VirtoMir')}</span>
                  {/if}
                </div>
              {/if}
            </div>
          {/each}
        {:else}
          <!-- Single VM or Initializing State -->
          {#each selectedVMs as vm (vm.id)}
            {@const isDone = currentJob?.state === 'succeeded'}
            {@const isFailed = currentJob?.state === 'failed'}
            {@const isRunning = currentJob?.state === 'running'}
            {@const domUuid = machineUuidFor(vm.name)}
            <div class="wave-item-card" class:done={isDone} class:failed={isFailed}>
              <div class="item-main">
                <span class="item-name">{singleTargetName || vm.name}</span>
                {#if isDone}
                  <span class="state-pill ok"><Check size={12} /> {t('Done')}</span>
                {:else if isFailed}
                  <span class="state-pill danger">{t('Failed')}</span>
                {:else if isRunning}
                  <span class="state-pill on"><RotateCw class="spin" size={12} /> {t('Converting…')}</span>
                {:else}
                  <span class="state-pill muted">{t('Pending')}</span>
                {/if}
              </div>

              {#if currentJob?.message}
                <div class="item-msg" class:is-err={isFailed}>{currentJob.message}</div>
              {/if}

              {#if isDone && domUuid}
                <div class="item-link">
                  <a
                    class="ui-btn sm ghost"
                    href={machineHref(domUuid)}
                    onclick={router.link}
                  >
                    <ExternalLink size={13} /> {t('Open machine')}
                  </a>
                </div>
              {/if}
            </div>
          {/each}
        {/if}
      </div>

      <div class="step-footer">
        <div class="left-actions">
          {#if waveStats.hasFailures && waveStats.finished}
            <button
              class="ui-btn danger"
              disabled={isRetrying}
              onclick={retryFailedWave}
            >
              {#if isRetrying}<RotateCw class="spin" size={14} />{:else}<RotateCw size={14} />{/if}
              {t('Retry failed')}
            </button>
          {/if}
        </div>
        <div class="right-actions">
          <button class="ui-btn ghost" onclick={resetWizard}>
            {t('Migrate more machines')}
          </button>
          <a class="ui-btn primary" href={router.scoped('/machines')} onclick={router.link}>
            {t('Go to Machines')} →
          </a>
        </div>
      </div>
    </section>
  {/if}
</div>

<style>
  .back-link {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    margin-bottom: 8px;
    color: var(--text-3);
    font-size: 13px;
    font-weight: 600;
    text-decoration: none;
  }
  .back-link:hover {
    color: var(--text);
  }
  .lead {
    margin: 0;
    color: var(--text-3);
    font-size: 13.5px;
  }

  /* Stepper Nav */
  .wizard-nav {
    display: flex;
    gap: 8px;
    margin: 20px 0 24px;
    flex-wrap: wrap;
  }
  .step-pill {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    padding: 8px 16px;
    border: var(--frame) solid var(--border);
    background: var(--surface);
    color: var(--text-3);
    font-family: var(--font-display);
    font-size: 13.5px;
    font-weight: 700;
    cursor: pointer;
    box-shadow: var(--bevel);
  }
  .step-pill:disabled {
    cursor: not-allowed;
    opacity: 0.5;
  }
  .step-pill.active {
    border-color: var(--ink);
    background: var(--bg-elev);
    color: var(--text);
    box-shadow: var(--bevel-in), 2px 2px 0 var(--ink);
  }
  .step-pill.completed {
    border-color: var(--ok);
    color: var(--ok);
  }
  .step-pill .num {
    display: inline-grid;
    place-items: center;
    width: 20px;
    height: 20px;
    border-radius: 50%;
    background: var(--border);
    color: var(--text);
    font-size: 11px;
    font-weight: 800;
  }
  .step-pill.active .num {
    background: var(--accent);
    color: var(--accent-contrast);
  }
  .step-pill.completed .num {
    background: var(--ok);
    color: #fff;
  }

  /* Cards */
  .step-card {
    padding: 24px;
    border: var(--frame) solid var(--ink);
    box-shadow: var(--shadow-hard);
  }
  .card-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    margin-bottom: 6px;
  }
  .card-header h2 {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 0;
    font-size: 18px;
  }
  .hint {
    margin: 0 0 18px;
    color: var(--text-3);
    font-size: 13px;
    line-height: 1.45;
  }

  /* Saved Connection Display */
  .saved-box {
    margin-bottom: 20px;
    padding: 16px;
    border: var(--frame) solid var(--border);
    background: var(--bg-elev);
  }
  .saved-details {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
    gap: 12px 20px;
    margin-bottom: 14px;
  }
  .detail-row {
    display: flex;
    flex-direction: column;
    gap: 3px;
  }
  .detail-row .label {
    color: var(--text-3);
    font-size: 11.5px;
    font-weight: 700;
    text-transform: uppercase;
  }
  .detail-row .val {
    color: var(--text);
    font-size: 13.5px;
  }
  .saved-actions {
    display: flex;
    gap: 10px;
    border-top: 1px solid var(--border);
    padding-top: 12px;
  }

  /* Form & Inputs */
  .form {
    display: flex;
    flex-direction: column;
    gap: 16px;
  }
  .row2 {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 16px;
  }
  .checkbox-row {
    display: flex;
    flex-direction: column;
    gap: 10px;
    margin: 4px 0 10px;
  }
  .checkbox-label {
    display: flex;
    align-items: center;
    gap: 8px;
    color: var(--text);
    font-size: 13px;
    cursor: pointer;
  }
  .test-banner {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-top: 14px;
    padding: 10px 14px;
    font-size: 13px;
    font-weight: 600;
  }
  .test-banner.ok {
    border: 1px solid var(--ok);
    background: rgba(30, 125, 87, 0.1);
    color: var(--ok);
  }
  .test-banner.err {
    border: 1px solid var(--danger);
    background: var(--danger-soft);
    color: var(--danger);
  }

  /* Toolbar */
  .inventory-toolbar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 16px;
    margin-bottom: 16px;
    flex-wrap: wrap;
  }
  .search-box {
    position: relative;
    flex: 1;
    min-width: 260px;
  }
  :global(.search-icon) {
    position: absolute;
    left: 10px;
    top: 50%;
    transform: translateY(-50%);
    color: var(--text-3);
    pointer-events: none;
  }
  .search-input {
    padding-left: 32px;
  }
  .selection-indicator {
    display: flex;
    align-items: center;
    gap: 12px;
  }
  .select-all-label {
    display: flex;
    align-items: center;
    gap: 6px;
    color: var(--text-2);
    font-size: 13px;
    cursor: pointer;
  }
  .count-pill {
    padding: 3px 8px;
    border: var(--frame) solid var(--ink);
    background: var(--surface);
    color: var(--text);
    font-family: var(--font-mono);
    font-size: 12px;
    font-weight: 700;
  }

  /* Tree & VM Table */
  .inventory-tree {
    display: flex;
    flex-direction: column;
    gap: 16px;
  }
  .tree-dc {
    border: var(--frame) solid var(--border);
    background: var(--bg-elev);
  }
  .dc-header {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 8px 12px;
    background: var(--surface);
    border-bottom: 1px solid var(--border);
    font-size: 13.5px;
    font-weight: 700;
  }
  .dc-count {
    color: var(--text-3);
    font-size: 12px;
    font-weight: normal;
  }
  .tree-host {
    padding: 10px 14px 14px;
  }
  .host-header {
    display: flex;
    align-items: center;
    gap: 6px;
    margin-bottom: 8px;
    color: var(--text-2);
    font-size: 12.5px;
    font-weight: 600;
  }
  .vm-table {
    width: 100%;
    border-collapse: collapse;
    font-size: 13px;
  }
  .vm-table th {
    padding: 6px 8px;
    border-bottom: 1px solid var(--border);
    color: var(--text-3);
    font-size: 11.5px;
    text-align: left;
    text-transform: uppercase;
  }
  .vm-table td {
    padding: 8px;
    border-bottom: 1px solid var(--border);
  }
  .w-check {
    width: 32px;
    text-align: center;
  }
  .vm-row {
    cursor: pointer;
  }
  .vm-row:hover:not(.disabled) {
    background: var(--surface-hover);
  }
  .vm-row.selected {
    background: rgba(35, 67, 184, 0.08);
  }
  .vm-row.disabled {
    cursor: not-allowed;
    opacity: 0.6;
  }
  .vm-name-cell strong {
    color: var(--text);
  }

  .state-pill {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    padding: 2px 7px;
    border-radius: 2px;
    font-size: 11px;
    font-weight: 700;
    text-transform: uppercase;
  }
  .state-pill.off {
    background: var(--surface);
    color: var(--text-3);
    border: 1px solid var(--border);
  }
  .state-pill.on {
    background: var(--accent-soft);
    color: var(--accent);
  }
  .state-pill.warn {
    background: rgba(168, 120, 10, 0.15);
    color: var(--warn);
  }
  .state-pill.ok {
    background: rgba(30, 125, 87, 0.15);
    color: var(--ok);
  }
  .state-pill.danger {
    background: var(--danger-soft);
    color: var(--danger);
  }
  .state-hint {
    display: block;
    margin-top: 2px;
    color: var(--danger);
    font-size: 11px;
  }

  /* Step 3 Settings Grid */
  .settings-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
    gap: 16px;
    margin-bottom: 24px;
  }
  .full-width {
    grid-column: 1 / -1;
  }

  /* Network Mapping */
  .network-mapping-section {
    margin-bottom: 24px;
    padding: 16px;
    border: var(--frame) solid var(--border);
    background: var(--bg-elev);
  }
  .network-mapping-section h3 {
    display: flex;
    align-items: center;
    gap: 6px;
    margin: 0 0 4px;
    font-size: 15px;
  }
  .mapping-table {
    width: 100%;
    margin-top: 10px;
    border-collapse: collapse;
  }
  .mapping-table td, .mapping-table th {
    padding: 8px 10px;
    font-size: 13px;
  }
  .w-arrow {
    width: 40px;
    color: var(--text-3);
    text-align: center;
  }

  /* Preflight */
  .preflight-section {
    margin-bottom: 24px;
    padding: 16px;
    border: var(--frame) solid var(--border);
    background: var(--bg-elev);
  }
  .preflight-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 12px;
  }
  .preflight-header h3 {
    margin: 0;
    font-size: 15px;
  }
  .preflight-box.loading {
    display: flex;
    align-items: center;
    gap: 10px;
    color: var(--text-2);
    font-size: 13px;
  }
  .status-banner {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-bottom: 14px;
    padding: 10px 14px;
    font-size: 13px;
    font-weight: 600;
  }
  .status-banner.ok {
    border: 1px solid var(--ok);
    background: rgba(30, 125, 87, 0.1);
    color: var(--ok);
  }
  .status-banner.err {
    border: 1px solid var(--danger);
    background: var(--danger-soft);
    color: var(--danger);
  }
  .preflight-items {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
    gap: 10px;
  }
  .vm-preflight-card {
    padding: 10px 12px;
    border: 1px solid var(--border);
    background: var(--surface);
  }
  .vm-pf-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
  }
  .issue-list {
    margin: 6px 0 0;
    padding-left: 18px;
    font-size: 12px;
    line-height: 1.4;
  }
  .issue-list.err {
    color: var(--danger);
  }
  .issue-list.warn {
    color: var(--warn);
  }

  /* Step 4 Live Progress */
  .overall-progress-bar {
    width: 100%;
    height: 10px;
    border: var(--frame) solid var(--ink);
    background: var(--surface);
    margin: 14px 0 10px;
    overflow: hidden;
  }
  .bar-fill {
    height: 100%;
    background: var(--ok);
    transition: width 0.3s ease;
  }
  .progress-summary-row {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 20px;
    font-size: 13.5px;
  }
  .lead-text {
    font-weight: 700;
    color: var(--text);
  }
  .job-status-msg {
    color: var(--text-3);
    font-size: 12.5px;
  }

  .wave-items-list {
    display: flex;
    flex-direction: column;
    gap: 8px;
    margin-bottom: 24px;
  }
  .wave-item-card {
    display: flex;
    flex-direction: column;
    gap: 6px;
    padding: 12px 16px;
    border: var(--frame) solid var(--border);
    background: var(--bg-elev);
  }
  .wave-item-card.done {
    border-color: var(--ok);
  }
  .wave-item-card.failed {
    border-color: var(--danger);
    background: var(--danger-soft);
  }
  .item-main {
    display: flex;
    align-items: center;
    justify-content: space-between;
  }
  .item-name {
    font-weight: 700;
    color: var(--text);
    font-size: 14px;
  }
  .item-msg {
    color: var(--text-2);
    font-size: 12.5px;
  }
  .item-msg.is-err {
    color: var(--danger);
  }
  .item-link {
    margin-top: 4px;
  }

  /* Footers */
  .step-footer {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 16px;
    margin-top: 24px;
    padding-top: 18px;
    border-top: 1px solid var(--border);
  }
  .right-actions {
    display: flex;
    gap: 10px;
  }

  /* Utility */
  :global(.spin) {
    animation: spin 1s linear infinite;
  }
  @keyframes spin {
    to {
      transform: rotate(360deg);
    }
  }
  .loading-state, .empty-state {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    padding: 40px 16px;
    gap: 10px;
    color: var(--text-3);
  }
  .mt-2 {
    margin-top: 8px;
  }
  .muted {
    color: var(--text-3);
  }
  .opt {
    color: var(--text-3);
    font-weight: normal;
  }
  .mono {
    font-family: var(--font-mono);
  }
  .font-bold {
    font-weight: 700;
  }

  @media (max-width: 768px) {
    .row2 {
      grid-template-columns: 1fr;
    }
    .wizard-nav {
      flex-direction: column;
    }
  }
</style>
