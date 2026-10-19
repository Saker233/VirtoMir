<script lang="ts">
  import { t } from '../i18n.svelte'
  // Showback pricing rates (ADR-0065): what one vCPU-hour, one GiB-hour of RAM and one
  // GiB-month of disk cost. One set per cluster.
  import { api, errorText } from '../api'
  import type { PricingConfig } from '../api'
  import { toasts } from '../toast.svelte'
  import Dialog from './Dialog.svelte'

  interface Props {
    pricing: PricingConfig
    onclose: () => void
  }
  let { pricing, onclose }: Props = $props()

  // The dialog is mounted fresh when it opens, so capturing the current pricing once is intended.
  // svelte-ignore state_referenced_locally
  let draft = $state<PricingConfig>({ ...pricing })
  let busy = $state(false)
  let error = $state('')

  const valid = $derived(
    Number.isFinite(draft.vcpuHour) && Number.isFinite(draft.ramGiBHour) && Number.isFinite(draft.diskGiBMonth) &&
      draft.vcpuHour >= 0 && draft.ramGiBHour >= 0 && draft.diskGiBMonth >= 0 && !!draft.currency.trim(),
  )

  async function save() {
    busy = true
    error = ''
    try {
      await api.saveCatalogPricing(draft)
      toasts.push({ kind: 'info', title: t('Pricing rates saved') })
      onclose()
    } catch (e) {
      error = errorText(e)
    } finally {
      busy = false
    }
  }
</script>

<Dialog open={true} title={t('Pricing rates')} subtitle={t('What machines cost on the Cost & Showback tab. One set for the whole cluster.')} width={520} onclose={onclose}>
  <div class="form">
    <div class="two">
      <label class="ui-field">
        <span class="label">{t('Currency symbol')}</span>
        <input class="ui-input mono" bind:value={draft.currency} placeholder="$" maxlength="4" spellcheck="false" />
      </label>
    </div>
    <div class="two">
      <label class="ui-field">
        <span class="label">{t('vCPU-hour')}</span>
        <input class="ui-input num" type="number" step="0.0001" min="0" bind:value={draft.vcpuHour} />
        <span class="hint">{t('One core running for one hour')}</span>
      </label>
      <label class="ui-field">
        <span class="label">{t('GiB RAM-hour')}</span>
        <input class="ui-input num" type="number" step="0.0001" min="0" bind:value={draft.ramGiBHour} />
        <span class="hint">{t('One GiB of memory running for one hour')}</span>
      </label>
      <label class="ui-field">
        <span class="label">{t('GiB disk-month')}</span>
        <input class="ui-input num" type="number" step="0.0001" min="0" bind:value={draft.diskGiBMonth} />
        <span class="hint">{t('One GiB provisioned for the whole month')}</span>
      </label>
    </div>
    {#if error}<div class="ui-error-box">{error}</div>{/if}
    <div class="btns">
      <button class="ui-btn ghost" onclick={onclose}>{t('Cancel')}</button>
      <button class="ui-btn primary" disabled={busy || !valid} onclick={() => void save()}>{t('Save')}</button>
    </div>
  </div>
</Dialog>

<style>
  .form {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .two {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 10px;
  }
  .num {
    text-align: right;
  }
  .btns {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
  }
</style>
