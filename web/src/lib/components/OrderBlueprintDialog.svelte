<script lang="ts">
  import { t } from '../i18n.svelte'
  // Ordering a machine from a blueprint: project, size preset, network, cloud-init
  // answers and lease length, with the monthly showback the preset costs (ADR-0065).
  import { api, errorText } from '../api'
  import type { Blueprint, CatalogRequest, PricingConfig, Project } from '../api'
  import { presetCost } from '../catalog'
  import { toasts } from '../toast.svelte'
  import Dialog from './Dialog.svelte'

  interface Props {
    blueprint: Blueprint
    projects: Project[]
    /** Networks the host offers; shown when the blueprint does not name any. */
    networks: string[]
    pricing: PricingConfig
    onclose: () => void
    /** Called after submitting so the page reloads its lists. */
    onordered: () => void
  }
  let { blueprint: bp, projects = [], networks = [], pricing, onclose, onordered }: Props = $props()

  let projectId = $state('')
  let presetId = $state('')
  let vmName = $state('')
  let network = $state('')
  let leaseDays = $state(30)
  let customLease = $state(false)
  let values = $state<Record<string, string>>({})
  let sshKeys = $state('')
  let password = $state('')
  let busy = $state(false)
  let error = $state('')

  // Slotted in fresh every time the dialog opens for a blueprint.
  let allowedProjects = $derived(bp.allowedProjects?.length ? projects.filter((p) => bp.allowedProjects!.includes(p.id)) : projects)
  let allowedNetworks = $derived(bp.allowedNetworks?.length ? bp.allowedNetworks : networks)
  let preset = $derived(bp.presets.find((p) => p.id === presetId) ?? bp.presets[0])
  let cost = $derived(preset ? presetCost(preset, pricing) : null)
  let maxDays = $derived(bp.maxLeaseDays || 365)
  let missingRequired = $derived((bp.cloudInitFields ?? []).some((f) => f.required && !String(values[f.key] ?? '').trim()))
  // Cloud images boot from a generic image with no baked-in credentials: at least one SSH
  // key or a password is required, unless the blueprint ships default SSH keys for access.
  let needCredentials = $derived(bp.sourceType === 'image' && !(bp.defaultSshKeys?.length))
  let valid = $derived(!!vmName.trim() && !!preset && !!projectId && !missingRequired && (!needCredentials || !!sshKeys.trim() || !!password.trim()))

  // Fill the defaults once per blueprint, keep whatever the user picked afterwards.
  let seededFor = $state('')
  $effect(() => {
    if (seededFor === bp.id) return
    seededFor = bp.id
    projectId = allowedProjects[0]?.id ?? ''
    presetId = bp.defaultPresetId ?? ''
    values = {}
    sshKeys = ''
    password = ''
    for (const f of bp.cloudInitFields ?? []) values[f.key] = f.default ?? ''
    leaseDays = bp.defaultLeaseDays || Math.min(30, maxDays)
    customLease = false
  })

  const hasFields = (b: Blueprint) => !!b.cloudInitFields?.length

  async function order() {
    if (!valid) return
    busy = true
    error = ''
    try {
      const req: Partial<CatalogRequest> = {
        blueprintId: bp.id,
        projectId: projectId,
        vmName: vmName.trim(),
        presetId: preset?.id,
        vcpus: preset?.vcpus,
        memoryMiB: preset?.memoryMiB,
        diskGiB: preset?.diskGiB,
        network: network || undefined,
        leaseDays,
        fieldValues: Object.keys(values).length ? values : undefined,
        sshKeys: sshKeys.trim() ? sshKeys.split('\n').map((s) => s.trim()).filter(Boolean) : undefined,
        password: password || undefined,
      }
      const saved = await api.createCatalogRequest(req)
      onclose()
      if (saved.status === 'pending_approval') {
        toasts.push({
          kind: 'info',
          title: t('Order {name} submitted', { name: vmName }),
          detail: t('Waiting for approval from a project admin.'),
        })
      } else {
        toasts.push({
          kind: 'info',
          title: t('{name} ordered', { name: vmName }),
          detail: t('{n} days lease, running now.', { n: leaseDays }),
        })
      }
      onordered()
    } catch (e) {
      error = errorText(e)
    } finally {
      busy = false
    }
  }
</script>

<Dialog open={true} title={t('Order {name}', { name: bp.name })} subtitle={bp.description} width={620} onclose={onclose}>
  <div class="form">
    <label class="ui-field">
      <span class="label">{t('Machine name')}</span>
      <input class="ui-input mono" bind:value={vmName} placeholder={t('a short host name')} spellcheck="false" />
    </label>

    <label class="ui-field">
      <span class="label">{t('Project')}</span>
      <select class="ui-input" bind:value={projectId}>
        {#each allowedProjects as p (p.id)}<option value={p.id}>{p.name}</option>{/each}
      </select>
    </label>

    <fieldset>
      <legend>{t('Size')}</legend>
      <div class="presets" role="radiogroup" aria-label={t('Size')}>
        {#each bp.presets as p (p.id)}
          <button type="button" role="radio" aria-checked={preset?.id === p.id} class="preset-card" class:on={preset?.id === p.id} onclick={() => (presetId = p.id)}>
            <span class="pname">{p.name}</span>
            <span class="pnum">{p.vcpus} vCPU</span>
            <span class="pnum">{(p.memoryMiB / 1024).toFixed(p.memoryMiB % 1024 ? 1 : 0)} GiB RAM</span>
            <span class="pnum">{p.diskGiB} GiB disk</span>
            <span class="pcost">{pricing.currency}{presetCost(p, pricing).toFixed(2)}/mo</span>
          </button>
        {/each}
      </div>
    </fieldset>

    <div class="two">
      <label class="ui-field">
        <span class="label">{t('Network')}</span>
        <select class="ui-input" bind:value={network}>
          {#if allowedNetworks.length === 0}<option value="">{t('Default choice')}</option>{/if}
          {#each allowedNetworks as n (n)}<option value={n}>{n}</option>{/each}
        </select>
      </label>
      <div class="ui-field">
        <span class="label">{t('Lease duration')}</span>
        <div class="lease" role="radiogroup" aria-label={t('Lease duration')}>
          {#each [7, 14, 30] as d (d)}
            {#if d <= maxDays}
              <button type="button" role="radio" aria-checked={leaseDays === d && !customLease} class="opt" onclick={() => { leaseDays = d; customLease = false }}>{t('{n} days', { n: d })}</button>
            {/if}
          {/each}
          <button type="button" role="radio" aria-checked={customLease} class="opt" onclick={() => (customLease = true)}>{t('Custom')}</button>
          {#if customLease}
            <input class="ui-input num" type="number" min="1" max={maxDays} bind:value={leaseDays} aria-label={t('Lease days')} />
          {/if}
        </div>
        {#if bp.maxLeaseDays}<span class="hint">{t('Up to {n} days', { n: bp.maxLeaseDays })}</span>{/if}
      </div>
    </div>

    {#if bp.sourceType === 'image'}
      <fieldset>
        <legend>{t('Access credentials')}</legend>
        {#if needCredentials}
          <p class="muted">{t('At least a key or a password is required so you can sign in to the cloud image.')}</p>
        {/if}
        <label class="ui-field">
          <span class="label">{t('SSH public keys')}</span>
          <textarea class="ui-input mono" rows="3" bind:value={sshKeys} placeholder={t('(one per line)')} spellcheck="false"></textarea>
        </label>
        <label class="ui-field">
          <span class="label">{t('Password')}</span>
          <input class="ui-input" type="password" bind:value={password} autocomplete="new-password" />
        </label>
      </fieldset>
    {/if}

    {#if bp.cloudInitFields?.length}
      <fieldset>
        <legend>{t('Set-up answers')}</legend>
        {#each bp.cloudInitFields as f (f.key)}
          <label class="ui-field">
            <span class="label">{f.label}{#if f.required} <small class="req">{t('required')}</small>{/if}</span>
            {#if f.type === 'ssh_key'}
              <textarea class="ui-input mono" rows="3" bind:value={values[f.key]} spellcheck="false"></textarea>
            {:else if f.type === 'password'}
              <input class="ui-input" type="password" bind:value={values[f.key]} autocomplete="new-password" />
            {:else if f.type === 'number'}
              <input class="ui-input num" type="number" bind:value={values[f.key]} />
            {:else if f.type === 'select'}
              <select class="ui-input" bind:value={values[f.key]}>
                {#each f.options ?? [] as o (o)}<option value={o}>{o}</option>{/each}
              </select>
            {:else}
              <input class="ui-input" bind:value={values[f.key]} />
            {/if}
          </label>
        {/each}
      </fieldset>
    {/if}

    {#if cost !== null && pricing}
      <div class="chip-row">
        <span class="cost-chip" title={t('What this size would show in Cost & Showback every month')}>
          {pricing.currency}{cost.toFixed(2)} {t('monthly')}
        </span>
      </div>
    {/if}

    {#if error}<div class="ui-error-box">{error}</div>{/if}
    <div class="btns">
      <button class="ui-btn ghost" onclick={onclose}>{t('Cancel')}</button>
      <button class="ui-btn primary" disabled={busy || !valid} onclick={() => void order()}>
        {bp.requireApproval ? t('Submit order') : t('Order')}
      </button>
    </div>
  </div>
</Dialog>

<style>
  .form {
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  fieldset {
    display: flex;
    flex-direction: column;
    gap: 8px;
    margin: 0;
    padding: 0;
    border: 0;
  }
  legend {
    padding: 0;
    color: var(--text-2);
    font-size: 12.5px;
    font-weight: 700;
  }
  .presets {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(150px, 1fr));
    gap: 8px;
  }
  .preset-card {
    display: flex;
    flex-direction: column;
    gap: 2px;
    padding: 10px 12px;
    border: var(--frame) solid var(--border-strong);
    border-radius: var(--radius-sm);
    background: var(--metal);
    cursor: pointer;
    text-align: left;
    color: var(--text);
  }
  .preset-card.on {
    border-color: var(--accent);
    box-shadow: 0 0 0 2px var(--accent-soft);
    background: var(--accent-soft);
  }
  .pname {
    font-weight: 700;
  }
  .pnum {
    color: var(--text-2);
    font-family: var(--font-mono);
    font-size: 12px;
  }
  .pcost {
    margin-top: 4px;
    font-family: var(--font-mono);
    font-size: 12.5px;
    color: var(--accent);
  }
  .two {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 10px;
  }
  .lease {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    align-items: center;
  }
  .opt {
    padding: 5px 10px;
    border: var(--frame) solid var(--border-strong);
    border-radius: var(--radius-sm);
    background: var(--metal);
    font-size: 12.5px;
    cursor: pointer;
    color: var(--text);
  }
  .opt[aria-checked='true'] {
    background: var(--invert-bg);
    color: var(--invert-fg);
  }
  .num {
    text-align: right;
    width: 90px;
  }
  .req {
    color: var(--danger);
    font-weight: 400;
  }
  .hint {
    color: var(--text-3);
    font-size: 12px;
  }
  .chip-row {
    display: flex;
    justify-content: flex-end;
  }
  .cost-chip {
    padding: 4px 10px;
    border: 1px solid var(--border-strong);
    border-radius: var(--radius-sm);
    background: color-mix(in srgb, var(--ok) 14%, transparent);
    color: var(--text);
    font-family: var(--font-mono);
    font-size: 13px;
  }
  .btns {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
  }
</style>
