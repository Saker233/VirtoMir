<script lang="ts">
  import { onMount } from 'svelte'
  import { ent, type DRSStatus, type DRSRecommendation, type AffinityRule, type DRSMode } from '../enterprise'
  import { toasts } from '../toast.svelte'
  import { t } from '../i18n.svelte'
  import Dialog from './Dialog.svelte'

  const MODE_HINTS: Record<DRSMode, string> = {
    manual: t('DRS only recommends; you apply each move.'),
    partially_automated: t('New machines are suggested the best host; moves stay recommendations.'),
    fully_automated: t('Every 5 minutes each host live-migrates one of its own machines when the cluster is out of balance (never the same machine twice within 30 minutes).'),
  }

  let status = $state<DRSStatus | null>(null)
  let loading = $state(false)
  let applying = $state<string | null>(null)

  // Rule dialog state
  let showRuleDialog = $state(false)
  let newRuleName = $state('')
  let newRuleKind = $state<'vm_anti_affinity' | 'vm_affinity' | 'vm_host_affinity'>('vm_anti_affinity')
  let newRuleMandatory = $state(true)
  let newRuleDomains = $state('')
  let newRuleTargetHost = $state('')

  async function load() {
    try {
      loading = true
      status = await ent.drsStatus()
    } catch {
      // DRS might not be enabled
    } finally {
      loading = false
    }
  }

  async function updateMode(mode: DRSMode) {
    if (!status) return
    try {
      await ent.saveDRSConfig({
        enabled: status.enabled,
        mode,
        thresholdStdDev: status.thresholdStdDev || 15.0,
      })
      toasts.push({ kind: 'info', title: t('DRS mode updated to {mode}', { mode }) })
      await load()
    } catch (e) {
      toasts.push({ kind: 'error', title: e instanceof Error ? e.message : String(e) })
    }
  }

  async function applyRecommendation(rec: DRSRecommendation) {
    try {
      applying = rec.id
      const res = await ent.applyDRSRecommendation(rec.id)
      toasts.push({ kind: 'info', title: res.message || t('Live migration initiated') })
      await load()
    } catch (e) {
      toasts.push({ kind: 'error', title: e instanceof Error ? e.message : String(e) })
    } finally {
      applying = null
    }
  }

  async function handleAddRule() {
    if (!newRuleName.trim()) {
      toasts.push({ kind: 'error', title: t('Rule name is required') })
      return
    }
    const domains = newRuleDomains
      .split(',')
      .map((s) => s.trim())
      .filter(Boolean)
    if (domains.length === 0) {
      toasts.push({ kind: 'error', title: t('Specify at least one VM UUID') })
      return
    }
    try {
      await ent.addDRSRule({
        name: newRuleName.trim(),
        kind: newRuleKind,
        mandatory: newRuleMandatory,
        domains,
        targetHost: newRuleKind === 'vm_host_affinity' ? newRuleTargetHost.trim() : undefined,
      })
      toasts.push({ kind: 'info', title: t('Affinity rule created') })
      showRuleDialog = false
      newRuleName = ''
      newRuleDomains = ''
      newRuleTargetHost = ''
      await load()
    } catch (e) {
      toasts.push({ kind: 'error', title: e instanceof Error ? e.message : String(e) })
    }
  }

  async function handleDeleteRule(id: string) {
    try {
      await ent.deleteDRSRule(id)
      toasts.push({ kind: 'info', title: t('Rule deleted') })
      await load()
    } catch (e) {
      toasts.push({ kind: 'error', title: e instanceof Error ? e.message : String(e) })
    }
  }

  onMount(() => {
    void load()
  })
</script>

{#if status && status.enabled}
  <div class="drs-deck" data-testid="drs-deck">
    <div class="deck-header">
      <div class="title-cluster">
        <span class="drs-icon">⚡</span>
        <div>
          <h3>{t('DRS (Distributed Resource Scheduler)')}</h3>
          <p class="subtitle">{t('Cluster workload balancing & affinity governance')}</p>
        </div>
      </div>

      <div class="controls-cluster">
        <div class="mode-selector">
          <label for="drs-mode-select">{t('Automation:')}</label>
          <select
            id="drs-mode-select"
            value={status.mode}
            onchange={(e) => updateMode(e.currentTarget.value as DRSMode)}
          >
            <option value="manual">{t('Manual (Recommendations Only)')}</option>
            <option value="partially_automated">{t('Partially Automated')}</option>
            <option value="fully_automated">{t('Fully Automated')}</option>
          </select>
          <span class="mode-hint">{MODE_HINTS[status.mode]}</span>
        </div>

        <button class="btn btn-sm" onclick={() => (showRuleDialog = true)}>
          + {t('Add Rule')}
        </button>
      </div>
    </div>

    <!-- Balance Meter -->
    <div class="balance-strip">
      <div class="gauge-card">
        <div class="gauge-val" class:optimal={(status.balanceScore ?? 0) >= 80} class:warning={(status.balanceScore ?? 0) < 60}>
          {(status.balanceScore ?? 0).toFixed(0)}%
        </div>
        <div class="gauge-label">{t('Cluster Balance Score')}</div>
      </div>

      <div class="metric-skew">
        <div class="skew-item">
          <span class="lbl">{t('CPU Skew (StdDev):')}</span>
          <span class="val font-mono">±{(status.cpuStdDev ?? 0).toFixed(1)}%</span>
        </div>
        <div class="skew-item">
          <span class="lbl">{t('RAM Skew (StdDev):')}</span>
          <span class="val font-mono">±{(status.memStdDev ?? 0).toFixed(1)}%</span>
        </div>
        <div class="skew-item">
          <span class="lbl">{t('Threshold:')}</span>
          <span class="val font-mono">±{(status.thresholdStdDev ?? 0).toFixed(1)}%</span>
        </div>
      </div>
    </div>

    <!-- Recommendations -->
    <div class="recommendations-section">
      <h4>
        {t('Optimization Recommendations')}
        {#if status.recommendations.length > 0}
          <span class="count-badge">{status.recommendations.length}</span>
        {/if}
      </h4>

      {#if status.recommendations.length === 0}
        <div class="empty-recs">
          <span class="check-icon">✓</span>
          {t('Cluster is optimally balanced. No migrations recommended.')}
        </div>
      {:else}
        <div class="rec-list">
          {#each status.recommendations as rec (rec.id)}
            <div class="rec-row" class:priority-high={rec.priority >= 4}>
              <div class="rec-stars" title="{rec.priority} of 5 priority">
                {'★'.repeat(rec.priority)}{'☆'.repeat(5 - rec.priority)}
              </div>
              <div class="rec-info">
                <div class="rec-action">
                  <strong>{rec.domainName}</strong>
                  <span class="arrow">➔</span>
                  <span class="host-from">{rec.sourceName}</span>
                  <span class="arrow">➔</span>
                  <span class="host-to">{rec.targetName}</span>
                </div>
                <div class="rec-reason">{rec.reason}</div>
              </div>

              <div class="rec-gain font-mono">
                +{rec.expectedGain}% {t('balance')}
              </div>

              <button
                class="btn btn-sm btn-primary"
                disabled={applying === rec.id}
                onclick={() => applyRecommendation(rec)}
              >
                {applying === rec.id ? t('Migrating…') : t('1-Click Apply')}
              </button>
            </div>
          {/each}
        </div>
      {/if}
    </div>

    <!-- Active Rules -->
    {#if status.rules.length > 0}
      <div class="rules-section">
        <h4>{t('Affinity & Anti-Affinity Rules')}</h4>
        <div class="rules-chips">
          {#each status.rules as rule (rule.id)}
            <div class="rule-chip">
              <span class="kind-tag" class:anti={rule.kind === 'vm_anti_affinity'}>
                {rule.kind === 'vm_anti_affinity' ? 'ANTI-AFFINITY' : 'AFFINITY'}
              </span>
              <span class="name">{rule.name}</span>
              <span class="count">({rule.domains.length} VMs)</span>
              <button
                class="del-btn"
                title={t('Delete rule')}
                onclick={() => handleDeleteRule(rule.id)}
              >
                ×
              </button>
            </div>
          {/each}
        </div>
      </div>
    {/if}
  </div>
{/if}

<!-- New Rule Dialog -->
<Dialog
  open={showRuleDialog}
  title={t('New Affinity / Anti-Affinity Rule')}
  onclose={() => (showRuleDialog = false)}
>
  <div class="dialog-form">
    <label>
      {t('Rule Name')}
      <input type="text" bind:value={newRuleName} placeholder="e.g. DNS Cluster Anti-Affinity" />
    </label>

    <label>
      {t('Constraint Type')}
      <select bind:value={newRuleKind}>
        <option value="vm_anti_affinity">{t('VM-VM Anti-Affinity (Separate Hosts)')}</option>
        <option value="vm_affinity">{t('VM-VM Affinity (Collocate on Same Host)')}</option>
        <option value="vm_host_affinity">{t('VM-Host Affinity (Pin to Specific Host)')}</option>
      </select>
    </label>

    <label>
      {t('Virtual Machine UUIDs (comma-separated)')}
      <textarea bind:value={newRuleDomains} placeholder="uuid-1, uuid-2"></textarea>
    </label>

    {#if newRuleKind === 'vm_host_affinity'}
      <label>
        {t('Target Host ID / Name')}
        <input type="text" bind:value={newRuleTargetHost} placeholder="e.g. host-01" />
      </label>
    {/if}

    <label class="checkbox-row">
      <input type="checkbox" bind:checked={newRuleMandatory} />
      <span>{t('Strict Mandatory Rule (Hard constraint)')}</span>
    </label>

    <div class="dialog-actions">
      <button class="btn btn-secondary" onclick={() => (showRuleDialog = false)}>
        {t('Cancel')}
      </button>
      <button class="btn btn-primary" onclick={handleAddRule}>
        {t('Create Rule')}
      </button>
    </div>
  </div>
</Dialog>

<style>
  .drs-deck {
    background: var(--surface);
    border: 2px solid var(--border);
    margin-bottom: var(--space-4);
    box-shadow: 4px 4px 0 var(--shadow);
  }

  .deck-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: var(--space-3) var(--space-4);
    background: var(--card);
    border-bottom: 1px solid var(--border);
    flex-wrap: wrap;
    gap: var(--space-2);
  }

  .title-cluster {
    display: flex;
    align-items: center;
    gap: var(--space-2);
  }

  .drs-icon {
    font-size: 1.5rem;
  }

  .title-cluster h3 {
    margin: 0;
    font-size: 1.05rem;
    font-weight: 700;
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }

  .subtitle {
    margin: 0;
    font-size: 0.78rem;
    color: var(--muted);
  }

  .controls-cluster {
    display: flex;
    align-items: center;
    gap: var(--space-3);
  }

  .mode-hint {
    flex-basis: 100%;
    font-size: 12px;
    color: var(--text-3);
  }
  .mode-selector {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--space-2);
    font-size: 0.82rem;
  }

  .mode-selector select {
    padding: 3px 6px;
    font-size: 0.82rem;
    background: var(--surface);
    border: 1px solid var(--border);
  }

  .balance-strip {
    display: flex;
    align-items: center;
    gap: var(--space-4);
    padding: var(--space-3) var(--space-4);
    background: var(--surface);
    border-bottom: 1px solid var(--border);
    flex-wrap: wrap;
  }

  .gauge-card {
    display: flex;
    flex-direction: column;
    align-items: center;
    padding: var(--space-2) var(--space-4);
    border: 1px solid var(--border);
    background: var(--card);
    min-width: 140px;
  }

  .gauge-val {
    font-size: 1.8rem;
    font-weight: 900;
    color: var(--accent);
  }

  .gauge-val.optimal {
    color: #4ade80;
  }

  .gauge-val.warning {
    color: #f87171;
  }

  .gauge-label {
    font-size: 0.72rem;
    text-transform: uppercase;
    color: var(--muted);
    font-weight: 600;
  }

  .metric-skew {
    display: flex;
    gap: var(--space-4);
    flex-wrap: wrap;
  }

  .skew-item {
    display: flex;
    flex-direction: column;
    gap: 2px;
  }

  .skew-item .lbl {
    font-size: 0.72rem;
    color: var(--muted);
    text-transform: uppercase;
  }

  .skew-item .val {
    font-size: 0.95rem;
    font-weight: 700;
  }

  .recommendations-section,
  .rules-section {
    padding: var(--space-3) var(--space-4);
    border-bottom: 1px solid var(--border);
  }

  .recommendations-section h4,
  .rules-section h4 {
    margin: 0 0 var(--space-2) 0;
    font-size: 0.85rem;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    display: flex;
    align-items: center;
    gap: var(--space-2);
  }

  .count-badge {
    background: var(--accent);
    color: #000;
    font-size: 0.7rem;
    padding: 1px 6px;
    font-weight: 800;
  }

  .empty-recs {
    padding: var(--space-3);
    font-size: 0.85rem;
    color: var(--muted);
    display: flex;
    align-items: center;
    gap: var(--space-2);
  }

  .check-icon {
    color: #4ade80;
    font-weight: 900;
  }

  .rec-list {
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
  }

  .rec-row {
    display: flex;
    align-items: center;
    gap: var(--space-3);
    padding: var(--space-2) var(--space-3);
    background: var(--card);
    border: 1px solid var(--border);
  }

  .rec-row.priority-high {
    border-left: 4px solid var(--accent);
  }

  .rec-stars {
    color: #fbbf24;
    font-size: 0.85rem;
    letter-spacing: 1px;
    white-space: nowrap;
  }

  .rec-info {
    flex: 1;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }

  .rec-action {
    font-size: 0.85rem;
    display: flex;
    align-items: center;
    gap: var(--space-1);
  }

  .arrow {
    color: var(--muted);
  }

  .host-from {
    color: #f87171;
  }

  .host-to {
    color: #4ade80;
  }

  .rec-reason {
    font-size: 0.75rem;
    color: var(--muted);
  }

  .rec-gain {
    font-size: 0.82rem;
    color: #4ade80;
    white-space: nowrap;
  }

  .rules-chips {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-2);
  }

  .rule-chip {
    display: flex;
    align-items: center;
    gap: var(--space-1);
    background: var(--card);
    border: 1px solid var(--border);
    padding: 3px 8px;
    font-size: 0.78rem;
  }

  .kind-tag {
    font-size: 0.65rem;
    padding: 1px 4px;
    background: #3b82f6;
    color: #fff;
    font-weight: 700;
  }

  .kind-tag.anti {
    background: #ef4444;
  }

  .rule-chip .name {
    font-weight: 600;
  }

  .rule-chip .count {
    color: var(--muted);
  }

  .del-btn {
    background: none;
    border: none;
    color: var(--muted);
    cursor: pointer;
    font-size: 1rem;
    padding: 0 3px;
  }

  .del-btn:hover {
    color: #ef4444;
  }

  .dialog-form {
    display: flex;
    flex-direction: column;
    gap: var(--space-3);
  }

  .dialog-form label {
    display: flex;
    flex-direction: column;
    gap: 4px;
    font-size: 0.82rem;
  }

  .dialog-form input,
  .dialog-form select,
  .dialog-form textarea {
    padding: 6px;
    font-size: 0.85rem;
    background: var(--surface);
    border: 1px solid var(--border);
  }

  .checkbox-row {
    flex-direction: row !important;
    align-items: center;
  }

  .dialog-actions {
    display: flex;
    justify-content: flex-end;
    gap: var(--space-2);
    margin-top: var(--space-2);
  }
</style>
