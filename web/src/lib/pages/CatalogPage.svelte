<script lang="ts">
  import { t } from '../i18n.svelte'
  // Catalog (ADR-0065): admins publish blueprints, members of a project order machines
  // from them, approvers answer the pending orders, showback turns what runs into money.
  import type { Component } from 'svelte'
  import ReceiptText from '@lucide/svelte/icons/receipt-text'
  import Globe from '@lucide/svelte/icons/globe'
  import Database from '@lucide/svelte/icons/database'
  import Terminal from '@lucide/svelte/icons/terminal'
  import Box from '@lucide/svelte/icons/box'
  import ShoppingCart from '@lucide/svelte/icons/shopping-cart'
  import Pencil from '@lucide/svelte/icons/pencil'
  import Plus from '@lucide/svelte/icons/plus'
  import Download from '@lucide/svelte/icons/download'
  import Coins from '@lucide/svelte/icons/coins'
  import ChevronDown from '@lucide/svelte/icons/chevron-down'
  import ChevronUp from '@lucide/svelte/icons/chevron-up'
  import { api, errorText, type Blueprint, type CatalogRequest, type CatalogLease, type Pool, type PricingConfig, type ShowbackReport, type VNetwork } from '../api'
  import { statusBadge, leaseDaysLeft, type BadgeKind } from '../catalog'
  import { projects as store } from '../projects.svelte'
  import { roleFor } from '../projects'
  import { machineHref, router } from '../router.svelte'
  import { session } from '../session.svelte'
  import { toasts } from '../toast.svelte'
  import { track } from '../jobs'
  import Dialog from '../components/Dialog.svelte'
  import ConfirmButton from '../components/ConfirmButton.svelte'
  import OrderBlueprintDialog from '../components/OrderBlueprintDialog.svelte'
  import EditBlueprintDialog from '../components/EditBlueprintDialog.svelte'
  import PricingSettingsDialog from '../components/PricingSettingsDialog.svelte'

  let { tab = '' }: { tab?: string } = $props()
  let view = $derived(['blueprints', 'requests', 'approvals', 'cost'].includes(tab) ? tab : 'blueprints')
  const go = (t2: string) => router.navigate(`/catalog${t2 ? '#' + t2 : ''}`, { replace: true })

  const ICONS: Record<string, Component<{ size?: number }>> = { globe: Globe, database: Database, terminal: Terminal }
  const bpIcon = (b: Blueprint): Component<{ size?: number }> => ICONS[b.icon ?? ''] ?? Box

  /** design system modifiers of .badge-brutal for each catalog badge kind. */
  const BADGE_CLASS: Record<BadgeKind, string> = { pending: 'warn', info: 'accent', ok: 'ok', muted: '', danger: 'danger' }

  // ---- Data ------------------------------------------------------------------------
  let blueprints = $state<Blueprint[]>([])
  let requests = $state<CatalogRequest[]>([])
  let leases = $state<Record<string, CatalogLease>>({})
  let pricing = $state<PricingConfig | null>(null)
  let pools = $state<Pool[]>([])
  let networks = $state<VNetwork[]>([])
  let loaded = $state(false)
  let loadError = $state('')

  const user = $derived(session.info?.user ?? '')
  const admin = $derived(session.can('admin'))
  /** Projects whose requests the user may answer (project admins + global admins). */
  let adminProjectIDs = $derived(store.list.filter((p) => roleFor(p, user) === 'admin').map((p) => p.id))
  let approver = $derived(admin || adminProjectIDs.length > 0)
  /** Projects the user may order into (memberships, ADR-0065). */
  let orderProjects = $derived(store.list.filter((p) => roleFor(p, user)))

  let pendingCount = $derived(
    requests.filter((r) => r.status === 'pending_approval' && (admin || adminProjectIDs.includes(r.projectId))).length,
  )
  let myRequests = $derived(user ? requests.filter((r) => r.requester === user) : requests)

  async function load() {
    try {
      const wantPools = session.can('admin')
      const [bps, reqs, pc, pls, nts] = await Promise.all([
        api.catalogBlueprints(),
        api.catalogRequests(approver ? {} : (user ? { requester: user } : {})),
        api.catalogPricing().catch(() => null),
        wantPools ? api.pools().catch(() => [] as Pool[]) : Promise.resolve([] as Pool[]),
        api.networks().catch(() => [] as VNetwork[]),
      ])
      await store.load()
      blueprints = bps
      requests = reqs
      pricing = pc
      pools = pls
      networks = nts
      loadError = ''
      loaded = true
      await loadLeases()
    } catch (e) {
      loadError = errorText(e)
      loaded = true
    }
  }
  $effect(() => {
    void load()
  })

  /** The list answer carries no leases: fetch expiry times for the provisioned ones. */
  async function loadLeases() {
    const active = requests.filter((r) => r.status === 'active' && r.machineUuid)
    if (active.length === 0) return
    const next: Record<string, CatalogLease> = {}
    for (const r of active) {
      try {
        const d = await api.catalogRequest(r.id)
        if (d.lease) next[r.id] = d.lease
      } catch {
        /* the lease record may be gone; no countdown then */
      }
    }
    leases = next
  }

  // ---- Blueprints ------------------------------------------------------------------
  /** undefined = closed, null = creating a new one, otherwise editing that blueprint. */
  let editing = $state<Blueprint | null | undefined>(undefined)
  let ordering = $state<Blueprint | null>(null)
  let pricingOpen = $state(false)

  async function removeBlueprint(b: Blueprint) {
    try {
      await api.deleteBlueprint(b.id)
      toasts.push({ kind: 'info', title: t('{name} deleted', { name: b.name }) })
      await load()
    } catch (e) {
      toasts.push({ kind: 'error', title: t('Could not delete {name}', { name: b.name }), detail: errorText(e) })
    }
  }

  // ---- Requests --------------------------------------------------------------------
  let extending = $state<CatalogRequest | null>(null)
  let extendDays = $state(7)
  let extendBusy = $state(false)
  let extendError = $state('')

  async function extendSave() {
    if (!extending) return
    extendBusy = true
    extendError = ''
    try {
      const l = await api.extendCatalogLease(extending.id, extendDays)
      if (l) leases[extending.id] = l
      toasts.push({ kind: 'info', title: t('Lease extended by {n} days', { n: extendDays }) })
      extending = null
      await load()
    } catch (e) {
      extendError = errorText(e)
    } finally {
      extendBusy = false
    }
  }

  async function cancelOrder(r: CatalogRequest) {
    try {
      await api.deleteCatalogRequest(r.id)
      toasts.push({ kind: 'info', title: t('{name} cancelled', { name: r.vmName }) })
      await load()
    } catch (e) {
      toasts.push({ kind: 'error', title: t('Not cancelled'), detail: errorText(e) })
    }
  }

  // ---- Approvals -------------------------------------------------------------------
  let review = $state<{ r: CatalogRequest; mode: 'approve' | 'reject' } | null>(null)
  let comment = $state('')
  let reviewBusy = $state(false)
  let reviewError = $state('')

  function openReview(r: CatalogRequest, mode: 'approve' | 'reject') {
    review = { r, mode }
    comment = ''
    reviewError = ''
  }

  async function sendReview() {
    if (!review) return
    reviewBusy = true
    reviewError = ''
    try {
      if (review.mode === 'approve') {
        const job = await api.approveCatalogRequest(review.r.id, comment)
        track(job, {
          success: t('{name} provisioned', { name: review.r.vmName }),
          onSuccess: () => void load(),
        })
      } else {
        await api.rejectCatalogRequest(review.r.id, comment)
      }
      toasts.push({ kind: 'info', title: review.mode === 'approve' ? t('Approved') : t('Rejected') })
      review = null
      await load()
    } catch (e) {
      reviewError = errorText(e)
    } finally {
      reviewBusy = false
    }
  }

  // ---- Cost & showback -----------------------------------------------------------------
  const started = new Date()
  let costYear = $state(started.getFullYear())
  let costMonth = $state(started.getMonth() + 1)
  let costProject = $state('')
  let report = $state<ShowbackReport | null>(null)
  let costError = $state('')
  let busyCost = $state(false)
  let expanded = $state<Record<string, boolean>>({})

  /** The last 12 months, most recent first, for the period picker. */
  let monthOptions = $derived.by(() => {
    const out: { y: number; m: number; label: string }[] = []
    const d = new Date(started)
    for (let i = 0; i < 12; i++) {
      out.push({ y: d.getFullYear(), m: d.getMonth() + 1, label: d.toLocaleString(undefined, { month: 'long' }) + ' ' + d.getFullYear() })
      d.setMonth(d.getMonth() - 1)
    }
    return out
  })

  $effect(() => {
    if (view !== 'cost') return
    void costReport()
  })

  async function costReport() {
    busyCost = true
    costError = ''
    try {
      report = (await api.catalogShowback({ year: costYear, month: costMonth, project: costProject || undefined })) as ShowbackReport
    } catch (e) {
      costError = errorText(e)
      report = null
    } finally {
      busyCost = false
    }
  }

  const money = (v: number, currency: string) => `${currency}${v.toFixed(2)}`

  async function exportShowback(format: 'csv' | 'html') {
    try {
      const text = (await api.catalogShowback({ year: costYear, month: costMonth, project: costProject || undefined, format })) as string
      const blob = new Blob([text], { type: format === 'csv' ? 'text/csv;charset=utf-8' : 'text/html;charset=utf-8' })
      const a = document.createElement('a')
      a.href = URL.createObjectURL(blob)
      a.download = `showback-${costYear}-${String(costMonth).padStart(2, '0')}.${format}`
      a.click()
      URL.revokeObjectURL(a.href)
    } catch (e) {
      toasts.push({ kind: 'error', title: t('Could not export'), detail: errorText(e) })
    }
  }

  let totals = $derived.by(() => {
    const p = report
    if (!p) return { vcpus: 0, ramGiB: 0, disk: 0 }
    return {
      vcpus: p.projects.reduce((a, x) => a + x.totalVcpus, 0),
      ramGiB: p.projects.reduce((a, x) => a + x.totalMemoryMiB, 0) / 1024,
      disk: p.projects.reduce((a, x) => a + x.totalDiskGiB, 0),
    }
  })

  const fmtDate = (s: string) => new Date(s).toLocaleDateString(undefined, { dateStyle: 'medium' })
</script>

<svelte:head><title>{t('Catalog · VirtoMir')}</title></svelte:head>

<header class="page-head">
  <div>
    <div class="title-row">
      <ReceiptText size={22} />
      <h1>{t('Catalog')}</h1>
    </div>
    <p class="lead">{t('Order machines from published blueprints, follow the approvals and see what they cost.')}</p>
  </div>
  {#if view === 'blueprints' && session.can('admin')}
    <button class="ui-btn primary" onclick={() => (editing = null)}><Plus size={15} /> {t('New blueprint')}</button>
  {/if}
</header>

{#if loadError}<div class="ui-error-box">{loadError}</div>{/if}

<div class="tabs" role="tablist" aria-label={t('Catalog sections')}>
  <button role="tab" aria-selected={view === 'blueprints'} class:on={view === 'blueprints'} onclick={() => go('blueprints')}>{t('Blueprints')}</button>
  <button role="tab" aria-selected={view === 'requests'} class:on={view === 'requests'} onclick={() => go('requests')}>{t('My Requests')}</button>
  {#if approver}
    <button role="tab" aria-selected={view === 'approvals'} class:on={view === 'approvals'} onclick={() => go('approvals')}>
      {t('Approvals')}
      {#if pendingCount > 0}<span class="count">{pendingCount}</span>{/if}
    </button>
  {/if}
  <button role="tab" aria-selected={view === 'cost'} class:on={view === 'cost'} onclick={() => go('cost')}>{t('Cost & Showback')}</button>
</div>

{#if !loaded}
  <div class="empty">{t('Loading…')}</div>
{:else if view === 'blueprints'}
  {#if blueprints.length === 0}
    <div class="empty">
      {t('No blueprints yet.')}
      {#if session.can('admin')}
        <button class="link" onclick={() => (editing = null)}>{t('Publish the first one')}</button>
      {/if}
    </div>
  {:else}
    <div class="cards">
      {#each blueprints as b (b.id)}
        {@const Icon = bpIcon(b)}
        <section class="ui-panel card">
          <div class="card-head">
            <span class="iconbox"><Icon size={18} /></span>
            <div class="card-title">
              <span class="name">{b.name}</span>
              <span class="mono small id">{b.id}</span>
            </div>
            <span class="src" class:img={b.sourceType === 'image'}>{b.sourceType === 'image' ? t('Cloud Image') : t('Template')}</span>
          </div>
          {#if b.description}<p class="desc">{b.description}</p>{/if}
          <div class="pills">
            {#each b.presets as p (p.id)}
              <span class="pill mono" title="{p.vcpus} vCPU · {(p.memoryMiB / 1024).toFixed(0)} GiB · {p.diskGiB} GiB">{p.name}</span>
            {/each}
          </div>
          <div class="meta">
            {#if b.requireApproval}
              <span class="tag warn">{t('Requires Approval')}</span>
            {:else}
              <span class="tag ok">{t('Instant')}</span>
            {/if}
            {#if b.allowedProjects?.length}
              <span class="tag">{t('Allowed projects:')}</span>
            {:else}
              <span class="tag">{t('All projects')}</span>
            {/if}
            {#if b.maxLeaseDays}
              <span class="tag">{t('max lease {n}d', { n: b.maxLeaseDays })}</span>
            {/if}
          </div>
          <div class="card-btns">
            <button class="ui-btn primary sm" disabled={!orderProjects.length && !admin} onclick={() => (ordering = b)}>
              <ShoppingCart size={14} /> {t('Order')}
            </button>
            {#if session.can('admin')}
              <button class="ui-btn sm ghost" onclick={() => (editing = b)}><Pencil size={14} /> {t('Edit')}</button>
              <ConfirmButton
                label={t('Delete')}
                confirm={t('Delete {name}?', { name: b.name })}
                title={t('Delete {name}', { name: b.name })}
                failure={t('Could not delete {name}', { name: b.name })}
                run={() => removeBlueprint(b)}
              />
            {/if}
          </div>
        </section>
      {/each}
    </div>
  {/if}
{:else if view === 'requests'}
  {#if myRequests.length === 0}
    <div class="empty">
      {t('No requests yet.')}
      <button class="link" onclick={() => go('blueprints')}>{t('Order a machine')}</button>
    </div>
  {:else}
    <section class="ui-panel">
      <div class="table" role="table" aria-label={t('My Requests')}>
        <div class="colhead" role="row">
          <span role="columnheader">{t('Machine')}</span>
          <span role="columnheader">{t('Blueprint')}</span>
          <span role="columnheader">{t('Project')}</span>
          <span role="columnheader">{t('Requested')}</span>
          <span role="columnheader">{t('Status')}</span>
          <span role="columnheader">{t('Lease')}</span>
          <span role="columnheader" class="sr">{t('Actions')}</span>
        </div>
        {#each myRequests as r (r.id)}
          {@const sb = statusBadge(r.status)}
          <div class="row" role="row">
            <div role="cell">
              {#if r.machineUuid}
                <a class="machine-link" href={machineHref(r.machineUuid)} onclick={router.link}>{r.vmName}</a>
              {:else}
                <span class="name">{r.vmName}</span>
                {#if r.errorMessage}<div class="small bad">{r.errorMessage}</div>{/if}
              {/if}
            </div>
            <div role="cell"><span class="mono small">{r.blueprintId}</span></div>
            <div role="cell">{store.name(r.projectId) || r.projectId}</div>
            <div role="cell"><span class="small">{fmtDate(r.createdAt)}</span></div>
            <div role="cell">
              <span class="badge-brutal {BADGE_CLASS[sb.kind]}">{t(sb.label)}</span>
              {#if r.reviewComment && r.status !== 'pending_approval'}<div class="small muted">{r.reviewComment}</div>{/if}
            </div>
            <div role="cell">
              {#if leases[r.id]}
                {@const days = leaseDaysLeft(leases[r.id].expiry)}
                {#if days > 2}
                  {t('Expires in {n} days', { n: days })}
                {:else if days > 0}
                  <span class="warn-pill">{t('Expires in {n} days', { n: days })}</span>
                {:else}
                  <span class="bad">{t('Lease expired')}</span>
                {/if}
              {:else if r.machineUuid}
                <span class="small muted">{t('No lease record')}</span>
              {:else}
                <span class="small">{r.leaseDays ? t('{n} days', { n: r.leaseDays }) : '—'}</span>
              {/if}
            </div>
            <div role="cell" class="actions">
              {#if r.status === 'active' && leases[r.id]}
                <button class="ui-btn sm" onclick={() => (extending = r)}>{t('Extend')}</button>
              {/if}
              {#if r.status === 'pending_approval' && (r.requester === user || admin)}
                <ConfirmButton
                  label={t('Cancel')}
                  confirm={t('Cancel the order of {name}?', { name: r.vmName })}
                  failure={t('Not cancelled')}
                  run={() => cancelOrder(r)}
                />
              {/if}
            </div>
          </div>
        {/each}
      </div>
    </section>
  {/if}
{:else if view === 'approvals'}
  {@const pending = requests.filter((r) => r.status === 'pending_approval' && (admin || adminProjectIDs.includes(r.projectId)))}
  {#if pending.length === 0}
    <div class="empty">{t('Nothing waiting for approval.')}</div>
  {:else}
    <section class="ui-panel">
      <div class="table" role="table" aria-label={t('Approvals')}>
        <div class="colhead" role="row">
          <span role="columnheader">{t('Requester')}</span>
          <span role="columnheader">{t('Machine')}</span>
          <span role="columnheader">{t('Project')}</span>
          <span role="columnheader">{t('Blueprint')}</span>
          <span role="columnheader">{t('Resources')}</span>
          <span role="columnheader">{t('Lease')}</span>
          <span role="columnheader">{t('Requested')}</span>
          <span role="columnheader" class="sr">{t('Actions')}</span>
        </div>
        {#each pending as r (r.id)}
          <div class="row" role="row">
            <div role="cell"><span class="name">{r.requester}</span></div>
            <div role="cell"><span class="mono small">{r.vmName}</span></div>
            <div role="cell">{store.name(r.projectId) || r.projectId}</div>
            <div role="cell"><span class="mono small">{r.blueprintId}</span></div>
            <div role="cell"><span class="mono small">{r.vcpus} vCPU · {(r.memoryMiB / 1024).toFixed(0)} GiB · {r.diskGiB} GiB</span></div>
            <div role="cell">{r.leaseDays} {t('days')}</div>
            <div role="cell"><span class="small">{fmtDate(r.createdAt)}</span></div>
            <div role="cell" class="actions">
              <button class="ui-btn sm primary" onclick={() => openReview(r, 'approve')}>{t('Approve')}</button>
              <button class="ui-btn sm ghost" onclick={() => openReview(r, 'reject')}>{t('Reject')}</button>
            </div>
          </div>
        {/each}
      </div>
    </section>
  {/if}
{:else if view === 'cost'}
  <div class="cost-tools">
    <label class="ui-field">
      <span class="label">{t('Billing period')}</span>
      <select class="ui-input" value={`${costYear}-${costMonth}`} onchange={(e) => {
        const [y, m] = e.currentTarget.value.split('-').map(Number)
        costYear = y
        costMonth = m
      }}>
        {#each monthOptions as o (o.y + '-' + o.m)}
          <option value={`${o.y}-${o.m}`} selected={o.y === costYear && o.m === costMonth}>{o.label}</option>
        {/each}
      </select>
    </label>
    <label class="ui-field">
      <span class="label">{t('Project')}</span>
      <select class="ui-input" bind:value={costProject} onchange={() => void costReport()}>
        <option value="">{t('All projects')}</option>
        {#each store.list as p (p.id)}<option value={p.id}>{p.name}</option>{/each}
      </select>
    </label>
    <div class="cost-gap"></div>
    <button class="ui-btn sm" disabled={busyCost || !report} onclick={() => void exportShowback('csv')}>
      <Download size={13} /> {t('Export CSV')}
    </button>
    <button class="ui-btn sm" disabled={busyCost || !report} onclick={() => void exportShowback('html')}>
      <Download size={13} /> {t('Export HTML')}
    </button>
    {#if admin}
      <button class="ui-btn sm" disabled={!pricing} onclick={() => (pricingOpen = true)}>
        <Coins size={13} /> {t('Pricing rates')}
      </button>
    {/if}
  </div>

  {#if costError}
    <div class="ui-error-box">{costError}</div>
  {:else if !report || busyCost}
    <section class="ui-panel"><div class="empty">{busyCost ? t('Collecting…') : t('Loading…')}</div></section>
  {:else}
    <div class="cards">
      <section class="ui-panel stat"><span class="stat-label">{t('Total spend')}</span><span class="stat-num money">{money(report.totalCost, report.currency)}</span><span class="stat-sub">{t('{n} machines', { n: report.totalMachines })}</span></section>
      <section class="ui-panel stat"><span class="stat-label">{t('Machine count')}</span><span class="stat-num">{report.totalMachines}</span><span class="stat-sub">{report.projects.length} {t('projects')}</span></section>
      <section class="ui-panel stat"><span class="stat-label">{t('Total vCPUs')}</span><span class="stat-num">{totals.vcpus}</span><span class="stat-sub">{t('provisioned cores')}</span></section>
      <section class="ui-panel stat"><span class="stat-label">{t('Total RAM')}</span><span class="stat-num">{Math.round(totals.ramGiB)} GiB</span><span class="stat-sub">{t('provisioned memory')}</span></section>
      <section class="ui-panel stat"><span class="stat-label">{t('Total Disk')}</span><span class="stat-num">{totals.disk} GiB</span><span class="stat-sub">{t('provisioned disk')}</span></section>
    </div>

    <section class="ui-panel">
      {#if report.projects.length === 0}
        <div class="empty">{t('No machines were running in this period.')}</div>
      {:else}
        <div class="table" role="table" aria-label={t('Cost & Showback')}>
          <div class="colhead" role="row">
            <span role="columnheader">{t('Project')}</span>
            <span role="columnheader">{t('Machines')}</span>
            <span role="columnheader">{t('vCPUs')}</span>
            <span role="columnheader">{t('Total spend')}</span>
            <span role="columnheader">{t('Share')}</span>
            <span role="columnheader" class="sr">{t('Expand machine costs')}</span>
          </div>
          {#each report.projects as pc (pc.projectId)}
            <div class="row cost-row" role="row">
              <div role="cell"><span class="name">{pc.projectName}</span><div class="mono small muted">{pc.projectId}</div></div>
              <div role="cell">{pc.machineCount}</div>
              <div role="cell">{pc.totalVcpus}</div>
              <div role="cell">{money(pc.totalCost, report.currency)}</div>
              <div role="cell" class="share">
                <span class="meter"><span class="fill" style:width="{report.totalCost > 0 ? (pc.totalCost / report.totalCost) * 100 : 0}%"></span></span>
              </div>
              <div role="cell" class="actions">
                <button class="ui-btn sm ghost" aria-expanded={!!expanded[pc.projectId]} onclick={() => (expanded = { ...expanded, [pc.projectId]: !expanded[pc.projectId] })}>
                  {#if expanded[pc.projectId]}<ChevronUp size={14} />{:else}<ChevronDown size={14} />{/if}
                </button>
              </div>
            </div>
            {#if expanded[pc.projectId]}
              <div class="row detail" role="row">
                <div role="cell">
                  <table class="items">
                    <thead><tr><th>{t('Machine')}</th><th>{t('vCPUs')}</th><th>{t('Memory')}</th><th>{t('Disk')}</th><th>{t('Runtime')}</th><th>{t('vCPU')}</th><th>{t('RAM')}</th><th>{t('Disk')}</th><th>{t('Total')}</th></tr></thead>
                    <tbody>
                      {#each pc.machines as vm (vm.uuid)}
                        <tr>
                          <td><a class="machine-link" href={machineHref(vm.uuid)} onclick={router.link}>{vm.name}</a></td>
                          <td>{vm.vcpus}</td>
                          <td>{(vm.memoryMiB / 1024).toFixed(0)} GiB</td>
                          <td>{vm.diskGiB} GiB</td>
                          <td>{vm.runtimeHours.toFixed(0)} h</td>
                          <td>{money(vm.vcpuCost, report.currency)}</td>
                          <td>{money(vm.ramCost, report.currency)}</td>
                          <td>{money(vm.diskCost, report.currency)}</td>
                          <td><b>{money(vm.totalCost, report.currency)}</b></td>
                        </tr>
                      {/each}
                    </tbody>
                  </table>
                </div>
              </div>
            {/if}
          {/each}
        </div>
      {/if}
    </section>
  {/if}
{/if}

{#if ordering && pricing}
  <OrderBlueprintDialog blueprint={ordering} projects={orderProjects} networks={networks.map((n) => n.name)} pricing={pricing} onclose={() => (ordering = null)} onordered={() => void load()} />
{/if}

{#if editing !== undefined && session.can('admin')}
  <EditBlueprintDialog
    blueprint={editing}
    projects={store.list}
    pools={pools}
    networks={networks}
    onclose={() => (editing = undefined)}
    onsaved={() => void load()}
  />
{/if}

{#if pricingOpen && pricing}
  <PricingSettingsDialog pricing={pricing} onclose={() => (pricingOpen = false)} />
{/if}

<Dialog open={!!extending} title={t('Extend lease of {name}', { name: extending?.vmName ?? '' })} width={420} onclose={() => (extending = null)}>
  <div class="form">
    <label class="ui-field">
      <span class="label">{t('Days to add')}</span>
      <select class="ui-input" bind:value={extendDays}>
        {#each [1, 7, 14, 30] as d (d)}<option value={d}>{t('{n} days', { n: d })}</option>{/each}
      </select>
    </label>
    {#if extendError}<div class="ui-error-box">{extendError}</div>{/if}
    <div class="btns">
      <button class="ui-btn ghost" onclick={() => (extending = null)}>{t('Cancel')}</button>
      <button class="ui-btn primary" disabled={extendBusy} onclick={() => void extendSave()}>{t('Extend')}</button>
    </div>
  </div>
</Dialog>

<Dialog open={!!review} title={review?.mode === 'approve' ? t('Approve {name}', { name: review?.r.vmName ?? '' }) : t('Reject {name}', { name: review?.r.vmName ?? '' })} width={480} onclose={() => (review = null)}>
  {#if review}
    <div class="form">
      <p class="muted nowrap">{review.r.requester} · {store.name(review.r.projectId) || review.r.projectId} · <span class="mono small">{review.r.vcpus} vCPU · {(review.r.memoryMiB / 1024).toFixed(0)} GiB · {review.r.diskGiB} GiB</span></p>
      <label class="ui-field">
        <span class="label">{review.mode === 'approve' ? t('Comment (optional)') : t('Reason (required)')}</span>
        <textarea class="ui-input" rows="3" bind:value={comment}></textarea>
      </label>
      {#if reviewError}<div class="ui-error-box">{reviewError}</div>{/if}
      <div class="btns">
        <button class="ui-btn ghost" onclick={() => (review = null)}>{t('Cancel')}</button>
        <button
          class="ui-btn"
          class:primary={review.mode === 'approve'}
          disabled={reviewBusy || (review.mode === 'reject' && !comment.trim())}
          onclick={() => void sendReview()}
        >
          {review.mode === 'approve' ? t('Approve') : t('Reject')}
        </button>
      </div>
    </div>
  {/if}
</Dialog>

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
  .lead {
    margin: 0;
    color: var(--text-3);
    font-size: 13.5px;
  }
  .page-head {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 12px;
    margin-bottom: 18px;
  }
  .tabs {
    display: flex;
    gap: 6px;
    margin-bottom: 16px;
  }
  .tabs > button {
    display: inline-flex;
    align-items: center;
    gap: 7px;
    padding: 7px 14px;
    border: var(--frame) solid var(--border-strong);
    border-radius: var(--radius-sm);
    background: var(--metal);
    color: var(--text-2);
    font-size: 13.5px;
    font-weight: 600;
    cursor: pointer;
  }
  .tabs > button.on {
    background: var(--invert-bg);
    color: var(--invert-fg);
  }
  .count {
    min-width: 20px;
    padding: 0 5px;
    border: 1px solid var(--ink);
    background: var(--ochre);
    color: var(--ink);
    font-family: var(--font-mono);
    font-size: 11px;
    text-align: center;
  }
  .empty {
    padding: 40px 16px;
    color: var(--text-3);
    text-align: center;
  }
  .empty .link {
    display: inline-block;
    margin-left: 8px;
    border: 0;
    background: none;
    color: var(--accent);
    cursor: pointer;
  }

  /* Blueprints */
  .cards {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(320px, 1fr));
    gap: 12px;
  }
  .card {
    display: flex;
    flex-direction: column;
    gap: 10px;
    padding: 14px;
  }
  .card-head {
    display: flex;
    align-items: center;
    gap: 10px;
  }
  .iconbox {
    display: grid;
    place-items: center;
    width: 36px;
    height: 36px;
    flex: none;
    border: var(--frame) solid var(--ink);
    background: var(--accent-soft);
    color: var(--text);
  }
  .card-title {
    display: flex;
    flex-direction: column;
    min-width: 0;
    flex: 1;
  }
  .name {
    font-weight: 700;
    color: var(--text);
  }
  .id {
    color: var(--text-3);
    font-size: 11px;
  }
  .src {
    flex: none;
    padding: 2px 8px;
    border: 1px solid var(--border-strong);
    background: var(--surface-2);
    color: var(--text-2);
    font-size: 11px;
  }
  .src.img {
    background: color-mix(in srgb, var(--accent) 16%, transparent);
    color: var(--accent);
  }
  .desc {
    margin: 0;
    color: var(--text-2);
    font-size: 13px;
  }
  .pills {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }
  .pill {
    padding: 2px 8px;
    border: 1px solid var(--border-strong);
    background: var(--bg-elev);
    font-size: 11.5px;
  }
  .meta {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }
  .tag {
    padding: 2px 8px;
    border: 1px solid var(--border-strong);
    font-size: 11px;
    color: var(--text-2);
  }
  .tag.warn {
    background: color-mix(in srgb, var(--warn) 20%, transparent);
    color: var(--warn);
    border-color: var(--warn);
  }
  .tag.ok {
    background: color-mix(in srgb, var(--ok) 16%, transparent);
    color: var(--ok);
    border-color: var(--ok);
  }
  .card-btns {
    display: flex;
    justify-content: flex-end;
    gap: 6px;
    margin-top: auto;
  }

  /* Tables */
  .table {
    display: grid;
  }
  .colhead,
  .row {
    display: grid;
    grid-template-columns: minmax(140px, 1fr) minmax(110px, 0.8fr) minmax(100px, 0.7fr) minmax(90px, 0.6fr) minmax(110px, 0.8fr) minmax(120px, 0.8fr) 150px;
    gap: 12px;
    align-items: center;
  }
  .colhead {
    padding: 8px 4px;
    border-bottom: 1px solid var(--border-strong);
    color: var(--text-3);
    font-size: 11.5px;
    font-weight: 600;
  }
  .row {
    padding: 10px 4px;
    border-bottom: 1px solid var(--border);
  }
  .row:hover {
    background: var(--surface-hover);
  }
  .cost-row {
    grid-template-columns: minmax(160px, 1.2fr) 80px 70px minmax(100px, 0.8fr) 1fr 60px;
  }
  .row.detail {
    border-bottom: none;
    padding-top: 0;
  }
  .row.detail > [role='cell'] {
    grid-column: 1 / -1;
  }
  .items {
    width: 100%;
    border-collapse: collapse;
    font-size: 12.5px;
    margin: 6px 0 10px;
  }
  .items th {
    text-align: left;
    padding: 5px 8px;
    color: var(--text-3);
    font-size: 11px;
    border-bottom: 1px solid var(--border-strong);
  }
  .items td {
    padding: 5px 8px;
    border-bottom: 1px solid var(--border);
  }
  .actions {
    display: flex;
    justify-content: flex-end;
    gap: 6px;
  }
  .sr {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip-path: inset(50%);
  }
  .small {
    font-size: 11.5px;
  }
  .mono {
    font-family: var(--font-mono);
  }
  .muted {
    color: var(--text-3);
  }
  .warn-pill {
    color: var(--warn);
  }
  .bad {
    color: var(--danger);
  }
  .machine-link {
    color: var(--accent);
    text-decoration: none;
  }
  .machine-link:hover {
    text-decoration: underline;
  }

  /* Cost */
  .cost-tools {
    display: flex;
    align-items: flex-end;
    gap: 10px;
    margin-bottom: 16px;
    flex-wrap: wrap;
  }
  .cost-tools .ui-field {
    min-width: 200px;
  }
  .cost-gap {
    flex: 1;
  }
  .share {
    width: 100%;
  }
  .meter {
    display: block;
    width: 100%;
    height: 10px;
    border: 1px solid var(--border-strong);
    background: var(--surface-2);
  }
  .fill {
    display: block;
    height: 100%;
    background: var(--accent);
  }
  .stat {
    padding: 14px;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .stat-label {
    color: var(--text-3);
    font-size: 11.5px;
    font-weight: 600;
    text-transform: uppercase;
  }
  .stat-num {
    font-family: var(--font-mono);
    font-size: 22px;
    font-weight: 700;
    color: var(--text);
  }
  .stat-sub {
    color: var(--text-3);
    font-size: 11.5px;
  }
  .nowrap {
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .form {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .btns {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
  }
  @media (max-width: 900px) {
    .colhead,
    .row {
      grid-template-columns: 1fr 1fr;
    }
    .colhead > :nth-child(n + 3),
    .row > :nth-child(n + 3):not(.actions) {
      display: none;
    }
    .cost-row {
      grid-template-columns: 1fr auto;
    }
  }
</style>
