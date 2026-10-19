<script lang="ts">
  import { t } from '../i18n.svelte'
  import Rocket from '@lucide/svelte/icons/rocket'
  import ChevronDown from '@lucide/svelte/icons/chevron-down'
  import ChevronRight from '@lucide/svelte/icons/chevron-right'
  import KeyRound from '@lucide/svelte/icons/key-round'
  import {
    api,
    errorText,
    type ContentDeploySpec,
    type ContentItem,
    type ContentVersion,
    type Pool,
    type VNetwork,
  } from '../api'
  import { live } from '../live.svelte'
  import { track } from '../jobs'
  import { machineHref, router } from '../router.svelte'
  import { formatBytes } from '../format'
  import Dialog from './Dialog.svelte'

  interface Props {
    open: boolean
    item: ContentItem | null
    versionId?: string
    onclose?: () => void
    ondeployed?: () => void
  }
  let { open = $bindable(false), item = null, versionId = '', onclose = () => (open = false), ondeployed }: Props = $props()

  let selectedVersionId = $state('')
  let name = $state('')
  let targetPool = $state('default')
  let selectedNetwork = $state('default')
  let vcpus = $state(2)
  let ramGiB = $state(2)
  let start = $state(true)

  let enableCloudInit = $state(false)
  let ciHostname = $state('')
  let ciUser = $state('admin')
  let ciPassword = $state('')
  let ciSshKey = $state('')

  let pools = $state<Pool[]>([])
  let networks = $state<VNetwork[]>([])
  let busy = $state(false)
  let error = $state('')

  function uniqueName(base: string): string {
    const clean = base
      .toLowerCase()
      .replace(/[^a-z0-9_-]/g, '-')
      .replace(/-+/g, '-')
      .replace(/^-|-$/g, '') || 'vm'
    const taken = new Set((live.snapshot?.domains ?? []).map((d) => d.name))
    if (!taken.has(clean)) return clean
    for (let i = 1; ; i++) {
      const candidate = `${clean}-${i}`
      if (!taken.has(candidate)) return candidate
    }
  }

  $effect(() => {
    if (!open || !item) return
    error = ''
    busy = false

    const versions = item.versions ?? (item.latestVersion ? [item.latestVersion] : [])
    const initialVer = versions.find((v) => v.id === versionId || v.version === versionId) ?? item.latestVersion ?? versions[0]
    selectedVersionId = initialVer?.id ?? initialVer?.version ?? ''

    name = uniqueName(item.name)
    ciHostname = name
    ciUser = 'admin'
    ciPassword = ''
    ciSshKey = ''
    enableCloudInit = item.type === 'image'
    vcpus = 2
    ramGiB = 2
    start = true

    void (async () => {
      try {
        const [p, n] = await Promise.all([
          api.pools().catch(() => [] as Pool[]),
          api.networks().catch(() => [] as VNetwork[]),
        ])
        pools = p.filter((x) => x.active)
        if (pools.length > 0) {
          targetPool = pools.find((x) => x.name === 'default')?.name ?? pools[0].name
        }
        networks = n.filter((x) => x.active)
        if (networks.length > 0) {
          selectedNetwork = networks.find((x) => x.name === 'default')?.name ?? networks[0].name
        }
      } catch (e) {
        error = errorText(e)
      }
    })()
  })

  let nameValid = $derived(/^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$/.test(name))
  let nameTaken = $derived((live.snapshot?.domains ?? []).some((d) => d.name === name))
  let maxCPUs = $derived(Math.max(1, live.host?.cpus ?? 8))
  let maxRAM = $derived(Math.max(1, Math.floor((live.host?.memoryKiB ?? 8 << 20) / (1 << 20))))

  let ok = $derived(nameValid && !nameTaken && targetPool !== '' && vcpus >= 1 && ramGiB > 0)

  async function submit(e: SubmitEvent) {
    e.preventDefault()
    if (!item || !ok) return
    busy = true
    error = ''

    const spec: ContentDeploySpec = {
      versionId: selectedVersionId || undefined,
      name: name.trim(),
      pool: targetPool,
      network: selectedNetwork || undefined,
      vcpus,
      memoryMiB: Math.round(ramGiB * 1024),
      start,
      cloudInit: enableCloudInit
        ? {
            hostname: ciHostname.trim() || name.trim(),
            user: ciUser.trim() || 'admin',
            password: ciPassword.trim() || undefined,
            sshKeys: ciSshKey.trim() ? ciSshKey.trim().split('\n').map((k) => k.trim()).filter(Boolean) : undefined,
          }
        : undefined,
    }

    try {
      const job = await api.deployFromContent(item.id, spec)
      const deployedName = name.trim()
      onclose()
      track(job, {
        success: t('{name} is ready', { name: deployedName }),
        onSuccess: (j) => j.result && router.navigate(machineHref(j.result)),
      })
      ondeployed?.()
    } catch (e) {
      error = errorText(e)
      busy = false
    }
  }

  let versions = $derived(item?.versions ?? (item?.latestVersion ? [item.latestVersion] : []))
  let currentVer = $derived(versions.find((v) => v.id === selectedVersionId || v.version === selectedVersionId) ?? item?.latestVersion)
</script>

<Dialog
  {open}
  title={t('Deploy machine from {name}', { name: item?.name ?? '' })}
  subtitle={t('Provision a new virtual machine directly from this library item')}
  width={560}
  {onclose}
>
  {#if item}
    <form id="deploy-content-form" class="form" onsubmit={submit}>
      {#if error}
        <div class="ui-error-box">{error}</div>
      {/if}

      <!-- Version selection -->
      {#if versions.length > 1}
        <label class="ui-field">
          <span class="label">{t('Item version')}</span>
          <select class="ui-input" bind:value={selectedVersionId}>
            {#each versions as v (v.id || v.version)}
              <option value={v.id || v.version}>
                v{v.version} — {formatBytes(v.size)} ({new Date(v.created).toLocaleDateString()})
              </option>
            {/each}
          </select>
        </label>
      {:else if currentVer}
        <div class="version-banner">
          <span class="chip mono">v{currentVer.version}</span>
          <span class="v-meta">{formatBytes(currentVer.size)} · {currentVer.format.toUpperCase()}</span>
          <span class="v-sha mono" title={currentVer.sha256}>
            SHA: {currentVer.sha256.slice(0, 10)}…
          </span>
        </div>
      {/if}

      <!-- Machine Name -->
      <label class="ui-field">
        <span class="label">{t('Machine name')}</span>
        <input
          class="ui-input"
          bind:value={name}
          oninput={() => {
            if (!enableCloudInit || ciHostname === name) ciHostname = name
          }}
          required
          aria-invalid={!nameValid || nameTaken}
          spellcheck="false"
        />
        {#if nameTaken}
          <span class="hint warn">{t('A machine with this name already exists.')}</span>
        {:else if !nameValid}
          <span class="hint warn">{t('Letters, digits, \'.\', \'_\', \'+\' or \'-\'.')}</span>
        {/if}
      </label>

      <!-- Storage Pool and Network -->
      <div class="row-2">
        <label class="ui-field">
          <span class="label">{t('Target storage pool')}</span>
          <select class="ui-input" bind:value={targetPool} required>
            {#each pools as p (p.name)}
              <option value={p.name}>{p.name} ({p.type})</option>
            {/each}
          </select>
        </label>

        <label class="ui-field">
          <span class="label">{t('Network')}</span>
          <select class="ui-input" bind:value={selectedNetwork}>
            {#if networks.length === 0}
              <option value="default">default</option>
            {:else}
              {#each networks as n (n.name)}
                <option value={n.name}>{n.name}</option>
              {/each}
            {/if}
          </select>
        </label>
      </div>

      <!-- CPU and RAM -->
      <div class="row-2">
        <label class="ui-field">
          <span class="label">{t('vCPUs')}</span>
          <input class="ui-input" type="number" min={1} max={maxCPUs} bind:value={vcpus} required />
          <span class="hint">{t('Up to {count} vCPUs available', { count: maxCPUs })}</span>
        </label>

        <label class="ui-field">
          <span class="label">{t('RAM (GiB)')}</span>
          <input class="ui-input" type="number" min={1} max={maxRAM} bind:value={ramGiB} required />
          <span class="hint">{t('Up to {count} GiB host RAM', { count: maxRAM })}</span>
        </label>
      </div>

      <!-- Cloud-Init Collapsible -->
      <div class="ci-accordion">
        <button
          type="button"
          class="ci-toggle"
          onclick={() => (enableCloudInit = !enableCloudInit)}
        >
          <div class="ci-toggle-left">
            <KeyRound size={15} />
            <strong>{t('Cloud-Init (initial boot setup)')}</strong>
          </div>
          {#if enableCloudInit}
            <ChevronDown size={15} />
          {:else}
            <ChevronRight size={15} />
          {/if}
        </button>

        {#if enableCloudInit}
          <div class="ci-fields">
            <label class="ui-field">
              <span class="label">{t('Hostname')}</span>
              <input class="ui-input" bind:value={ciHostname} placeholder={name} />
            </label>

            <div class="row-2">
              <label class="ui-field">
                <span class="label">{t('Username')}</span>
                <input class="ui-input" bind:value={ciUser} placeholder="admin" required />
              </label>

              <label class="ui-field">
                <span class="label">{t('Password')} <span class="opt">{t('(optional)')}</span></span>
                <input class="ui-input" type="password" bind:value={ciPassword} placeholder="••••••••" />
              </label>
            </div>

            <label class="ui-field">
              <span class="label">{t('SSH public keys')} <span class="opt">{t('(optional)')}</span></span>
              <textarea
                class="ui-input text-area"
                bind:value={ciSshKey}
                placeholder="ssh-ed25519 AAAAC3NzaC1lZDI1NTE5..."
                rows={2}
              ></textarea>
            </label>
          </div>
        {/if}
      </div>

      <!-- Start toggle -->
      <label class="ui-switch" title={t('Start machine immediately after cloning')}>
        <input type="checkbox" bind:checked={start} />
        <span>{t('Start machine immediately')}</span>
      </label>
    </form>
  {/if}

  {#snippet footer()}
    <button type="button" class="ui-btn" onclick={onclose}>{t('Cancel')}</button>
    <button
      type="submit"
      form="deploy-content-form"
      class="ui-btn primary"
      disabled={!ok || busy}
    >
      <Rocket size={15} />
      {busy ? t('Deploying…') : t('Deploy')}
    </button>
  {/snippet}
</Dialog>

<style>
  .form {
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .row-2 {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 12px;
  }
  .version-banner {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 8px 12px;
    background: var(--surface-2);
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    font-size: 12.5px;
  }
  .v-meta {
    color: var(--text-2);
    font-size: 12px;
  }
  .v-sha {
    margin-left: auto;
    font-size: 11px;
    color: var(--text-3);
  }
  .ci-accordion {
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    background: var(--surface-2);
    overflow: hidden;
  }
  .ci-toggle {
    width: 100%;
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 10px 14px;
    background: none;
    border: none;
    cursor: pointer;
    font-size: 13px;
    color: var(--text);
  }
  .ci-toggle:hover {
    background: var(--bg-elev);
  }
  .ci-toggle-left {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .ci-fields {
    padding: 12px 14px;
    border-top: 1px solid var(--border);
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .text-area {
    resize: vertical;
    font-family: var(--font-mono);
    font-size: 12px;
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
  .hint.warn {
    color: var(--danger, #ff4d4f);
  }
</style>
