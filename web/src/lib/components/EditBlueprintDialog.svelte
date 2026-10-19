<script lang="ts">
  import { t } from '../i18n.svelte'
  // Publishing a blueprint (ADR-0065): where its machine comes from, sizes it may be ordered
  // in, the answers it asks for, which projects and networks may order it and its lease rules.
  import { api, errorText, type Blueprint, type InputField, type Pool, type Project, type VNetwork } from '../api'
  import { toasts } from '../toast.svelte'
  import Dialog from './Dialog.svelte'
  import Plus from '@lucide/svelte/icons/plus'
  import Trash from '@lucide/svelte/icons/trash'

  interface Props {
    /** null = a new blueprint. */
    blueprint: Blueprint | null
    projects: Project[]
    pools: Pool[]
    networks: VNetwork[]
    onclose: () => void
    onsaved: () => void
  }
  let { blueprint, projects, pools, networks, onclose, onsaved }: Props = $props()

  const FIELD_TYPES: InputField['type'][] = ['string', 'password', 'ssh_key', 'number', 'select']

  function blank(): Blueprint {
    return {
      id: '',
      name: '',
      description: '',
      icon: 'box',
      sourceType: 'template',
      sourceRef: '',
      targetPool: '',
      presets: [{ id: 'small', name: 'Base', vcpus: 2, memoryMiB: 4096, diskGiB: 40 }],
      defaultPresetId: 'small',
      allowedNetworks: [],
      cloudInitFields: [],
      allowedProjects: [],
      requireApproval: false,
      defaultLeaseDays: 30,
      maxLeaseDays: 90,
      created: '',
      updated: '',
    }
  }

  let b = $state<Blueprint>(blank())
  let isNew = $state(true)
  let busy = $state(false)
  let error = $state('')

  // A fresh draft each time the dialog opens for a different blueprint.
  $effect(() => {
    const bp = blueprint
    isNew = !bp
    b = bp ? { ...bp, presets: bp.presets.map((p) => ({ ...p })), cloudInitFields: (bp.cloudInitFields ?? []).map((f) => ({ ...f })), allowedNetworks: [...(bp.allowedNetworks ?? [])], allowedProjects: [...(bp.allowedProjects ?? [])] } : blank()
    error = ''
  })

  let idValid = $derived(isNew ? /^[a-z0-9][a-z0-9-]{1,38}[a-z0-9]$/.test(b.id) : true)
  let nameValid = $derived(!!b.name.trim() && !!b.sourceRef.trim())
  let presetsValid = $derived(b.presets.length > 0 && b.presets.every((p) => !!p.id && !!p.name && Number.isFinite(p.vcpus) && p.vcpus > 0 && p.memoryMiB > 0 && p.diskGiB > 0))
  // Deleting the marked preset must not leave a dangling defaultPresetId.
  let defaultPresetValid = $derived(!b.presets.length || b.presets.some((p) => p.id === b.defaultPresetId))
  let leaseValid = $derived(!b.maxLeaseDays || !b.defaultLeaseDays || b.defaultLeaseDays <= b.maxLeaseDays)
  let valid = $derived(idValid && nameValid && presetsValid && defaultPresetValid && leaseValid)

  function toggle(list: string[], name: string): string[] {
    return list.includes(name) ? list.filter((n) => n !== name) : [...list, name]
  }

  function newPreset() {
    const id = `preset-${b.presets.length + 1}`
    b.presets.push({ id, name: `Size ${b.presets.length + 1}`, vcpus: 2, memoryMiB: 2048, diskGiB: 20 })
    b.defaultPresetId = b.defaultPresetId || id
  }
  function newField() {
    b.cloudInitFields!.push({ key: `field${b.cloudInitFields!.length + 1}`, label: 'New field', type: 'string', required: false, default: '' })
  }

  async function save() {
    if (!valid) return
    busy = true
    error = ''
    try {
      if (isNew) await api.createBlueprint({ ...b, updated: '' })
      else await api.updateBlueprint(b.id, { ...b, updated: '' })
      toasts.push({ kind: 'info', title: t('{name} saved', { name: b.name }) })
      b = blank() // the $effect fills it again the next time the dialog opens
      onsaved()
      onclose()
    } catch (e) {
      error = errorText(e)
    } finally {
      busy = false
    }
  }
</script>

<Dialog open={true} title={isNew ? t('New blueprint') : t('Edit {name}', { name: b.name })} width={680} onclose={onclose}>
  <div class="form">
    {#if isNew}
      <label class="ui-field">
        <span class="label">{t('ID')}</span>
        <input class="ui-input mono" bind:value={b.id} placeholder="web-server" spellcheck="false" />
        <span class="hint">{t('Lower-case letters, digits and dashes; used in API paths.')}</span>
      </label>
    {/if}
    <div class="two">
      <label class="ui-field">
        <span class="label">{t('Name')}</span>
        <input class="ui-input" bind:value={b.name} />
      </label>
      <label class="ui-field">
        <span class="label">{t('Icon')}</span>
        <select class="ui-input" bind:value={b.icon}>
          <option value="box">{t('Box')}</option>
          <option value="globe">{t('Globe')}</option>
          <option value="database">{t('Database')}</option>
          <option value="terminal">{t('Terminal')}</option>
        </select>
      </label>
    </div>
    <label class="ui-field">
      <span class="label">{t('Description')}</span>
      <input class="ui-input" bind:value={b.description} />
    </label>

    <fieldset>
      <legend>{t('Source')}</legend>
      <div class="two">
        <label class="ui-field">
          <span class="label">{t('Made from')}</span>
          <select class="ui-input" bind:value={b.sourceType}>
            <option value="template">{t('Template')}</option>
            <option value="image">{t('Cloud Image')}</option>
          </select>
        </label>
        <label class="ui-field">
          <span class="label">{b.sourceType === 'template' ? t('Template name') : t('Cloud image URL')}</span>
          <input class="ui-input mono" bind:value={b.sourceRef} placeholder={b.sourceType === 'template' ? 'tmpl-ubuntu-24.04' : 'https://cloud.debian.org/images/…'} spellcheck="false" />
        </label>
        <label class="ui-field">
          <span class="label">{t('Target pool')}</span>
          <select class="ui-input" bind:value={b.targetPool}>
            <option value="">{t('Project default')}</option>
            {#each pools as p (p.name)}<option value={p.name}>{p.name}</option>{/each}
          </select>
        </label>
      </div>
    </fieldset>

    <fieldset>
      <legend>{t('Size presets')} <small>{t('the marked one is ordered when nothing is chosen')}</small></legend>
      {#each b.presets as p, i (i)}
        <div class="preset-row">
          <label class="def-radio" title={t('Default size')}>
            <input type="radio" name="defaultPreset" checked={b.defaultPresetId === p.id} onchange={() => (b.defaultPresetId = p.id)} aria-label={t('Default size')} />
          </label>
          <input class="ui-input" bind:value={p.name} aria-label={t('Name')} />
          <input class="ui-input num" type="number" min="1" bind:value={p.vcpus} aria-label={t('vCPUs')} />
          <input class="ui-input num" type="number" min="1" bind:value={p.memoryMiB} aria-label={t('Memory (MiB)')} />
          <input class="ui-input num" type="number" min="1" bind:value={p.diskGiB} aria-label={t('Disk (GiB)')} />
          <button class="ui-btn sm ghost" type="button" onclick={() => { b.presets.splice(i, 1); if (b.defaultPresetId === p.id) b.defaultPresetId = b.presets[0]?.id ?? '' }} title={t('Remove this preset')} aria-label={t('Remove this preset')}>
            <Trash size={14} />
          </button>
        </div>
      {/each}
      <button class="ui-btn sm" type="button" onclick={newPreset}><Plus size={14} /> {t('Add size preset')}</button>
      {#if !presetsValid}<span class="hint bad">{t('Every preset needs a name, vCPUs, memory and disk.')}</span>{/if}
      {#if presetsValid && !defaultPresetValid}<span class="hint bad">{t('Mark one preset as the default size.')}</span>{/if}
    </fieldset>

    <fieldset>
      <legend>{t('Set-up questions (cloud-init)')}</legend>
      {#each b.cloudInitFields as f, i (i)}
        <div class="field-row">
          <input class="ui-input" bind:value={f.label} placeholder={t('Label')} aria-label={t('Label')} />
          <input class="ui-input mono" bind:value={f.key} spellcheck="false" aria-label={t('Variable key')} />
          <select class="ui-input" bind:value={f.type} aria-label={t('Field type')}>
            {#each FIELD_TYPES as ft (ft)}<option value={ft}>{ft}</option>{/each}
          </select>
          <label class="check"><input type="checkbox" bind:checked={f.required} /> {t('required')}</label>
          <button class="ui-btn sm ghost" type="button" onclick={() => b.cloudInitFields!.splice(i, 1)} title={t('Remove this field')} aria-label={t('Remove this field')}>
            <Trash size={14} />
          </button>
        </div>
      {/each}
      <button class="ui-btn sm" type="button" onclick={newField}><Plus size={14} /> {t('Add field')}</button>
    </fieldset>

    <fieldset>
      <legend>{t('Access')}</legend>
      <div class="checks">
        {#each projects as p (p.id)}
          <label class="check"><input type="checkbox" checked={b.allowedProjects!.includes(p.id)} onchange={() => (b.allowedProjects = toggle(b.allowedProjects!, p.id))} /> {p.name}</label>
        {:else}
          <span class="hint">{t('Leave empty: every project may order it.')}</span>
        {/each}
      </div>
      <div class="checks">
        {#each networks as n (n.name)}
          <label class="check"><input type="checkbox" checked={b.allowedNetworks!.includes(n.name)} onchange={() => (b.allowedNetworks = toggle(b.allowedNetworks!, n.name))} /> {n.name}</label>
        {:else}
          <span class="hint">{t('No networks on this host.')}</span>
        {/each}
      </div>
    </fieldset>

    <fieldset>
      <legend>{t('Approval and lease')}</legend>
      <label class="ui-switch"><input type="checkbox" bind:checked={b.requireApproval} /> {t('Every order needs approval from a project admin')}</label>
      <div class="two">
        <label class="ui-field"><span class="label">{t('Default lease (days)')}</span><input class="ui-input num" type="number" min="0" bind:value={b.defaultLeaseDays} /></label>
        <label class="ui-field"><span class="label">{t('Maximum lease (days, 0 = none)')}</span><input class="ui-input num" type="number" min="0" bind:value={b.maxLeaseDays} /></label>
      </div>
      {#if !leaseValid}
        <span class="hint bad">{t('Default lease cannot exceed maximum lease.')}</span>
      {/if}
    </fieldset>

    {#if error}<div class="ui-error-box">{error}</div>{/if}
    <div class="btns">
      <button class="ui-btn ghost" onclick={onclose}>{t('Cancel')}</button>
      <button class="ui-btn primary" disabled={busy || !valid} onclick={() => void save()}>{busy ? t('Saving…') : t('Save')}</button>
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
  legend small {
    color: var(--text-3);
    font-weight: 400;
  }
  .two {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 10px;
  }
  .preset-row,
  .field-row {
    display: grid;
    gap: 6px;
    align-items: center;
  }
  .preset-row {
    grid-template-columns: 24px 1fr 70px 100px 90px 30px;
  }
  .field-row {
    grid-template-columns: 1fr 0.9fr 110px auto 30px;
  }
  .def-radio {
    display: inline-flex;
    align-items: center;
    justify-content: center;
  }
  .def-radio input {
    accent-color: var(--accent);
  }
  .num {
    text-align: right;
  }
  .checks {
    display: flex;
    flex-wrap: wrap;
    gap: 6px 14px;
  }
  .check {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 13px;
  }
  .check input {
    accent-color: var(--accent);
  }
  .hint {
    color: var(--text-3);
    font-size: 12px;
  }
  .hint.bad {
    color: var(--danger);
  }
  .btns {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
  }
</style>
