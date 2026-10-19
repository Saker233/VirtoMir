<script lang="ts">
  import { t } from '../i18n.svelte'
  // Compliance & hardening (ADR-0064): the host's CIS Linux / STIG posture, FIPS 140 mode and
  // the tamper-evident audit log, verified over the wire.
  import ShieldCheck from '@lucide/svelte/icons/shield-check'
  import ShieldAlert from '@lucide/svelte/icons/shield-alert'
  import ShieldOff from '@lucide/svelte/icons/shield-off'
  import RefreshCw from '@lucide/svelte/icons/refresh-cw'
  import FileBarChart from '@lucide/svelte/icons/file-chart-column'
  import Download from '@lucide/svelte/icons/download'
  import Copy from '@lucide/svelte/icons/copy'
  import CircleCheck from '@lucide/svelte/icons/circle-check'
  import CircleX from '@lucide/svelte/icons/circle-x'
  import CircleAlert from '@lucide/svelte/icons/circle-alert'
  import TriangleAlert from '@lucide/svelte/icons/triangle-alert'
  import ChevronDown from '@lucide/svelte/icons/chevron-down'
  import Lock from '@lucide/svelte/icons/lock'
  import { api, errorText, type AuditVerification, type CheckSeverity, type CheckStatus, type FIPSInfo, type HostCompliance } from '../api'
  import { ent, type ReportFormat } from '../enterprise'
  import { session } from '../session.svelte'
  import { toasts } from '../toast.svelte'
  import { cluster } from '../cluster.svelte'
  import { copy } from '../machine'
  import Dialog from '../components/Dialog.svelte'

  type Tab = 'checks' | 'audit' | 'fips'
  type Pill = 'all' | 'fail' | 'warn' | 'pass'

  let tab = $state<Tab>('checks')

  // ---- Hardening checks ------------------------------------------------------------------
  let comp = $state<HostCompliance | null>(null)
  let comps = $state<HostCompliance[]>([])
  let pickHost = $state('')
  let error = $state('')
  let loading = $state(true)
  let scanning = $state(false)
  let filter = $state<Pill>('all')
  let query = $state('')
  let openId = $state('')

  const SEVERITIES: Record<CheckSeverity, string> = {
    critical: t('Critical'),
    high: t('High'),
    medium: t('Medium'),
    low: t('Low'),
  }
  const STATUSES: Record<CheckStatus, { label: string; icon: typeof CircleCheck }> = {
    pass: { label: t('Pass'), icon: CircleCheck },
    fail: { label: t('Fail'), icon: CircleX },
    warn: { label: t('Warning'), icon: TriangleAlert },
    error: { label: t('Error'), icon: CircleAlert },
  }
  const CATEGORIES: Record<string, string> = {
    SSH: t('SSH'),
    Virtualization: t('Virtualization'),
    Firewall: t('Firewall'),
    Kernel: t('Kernel'),
    System: t('System'),
  }
  const catLabel = (c: string) => CATEGORIES[c] ?? c

  async function loadChecks(refresh = false) {
    loading = true
    error = ''
    try {
      if (cluster.clustered) {
        comps = await api.clusterCompliance()
        if (!comps.some((c) => c.hostId === pickHost)) pickHost = comps[0]?.hostId ?? ''
      } else {
        comps = []
        comp = await api.complianceChecks(refresh)
      }
    } catch (e) {
      error = errorText(e)
    } finally {
      loading = false
    }
  }

  /**
   * The checks shown: in a cluster the member chosen in the picker, else the host that
   * serves the page.
   */
  let current = $derived.by(() => {
    if (!cluster.clustered) return comp
    return comps.find((c) => c.hostId === pickHost) ?? comps[0] ?? null
  })

  let filtered = $derived.by(() => {
    const list = current?.results ?? []
    const q = query.trim().toLowerCase()
    return list.filter((c) => {
      const hit =
        filter === 'all' ? true : filter === 'fail' ? c.status === 'fail' || c.status === 'error' : filter === 'warn' ? c.status === 'warn' : c.status === 'pass'
      if (!hit) return false
      return !q || c.title.toLowerCase().includes(q) || c.category.toLowerCase().includes(q) || c.id.toLowerCase().includes(q)
    })
  })

  async function runScan() {
    scanning = true
    try {
      const res = await api.runComplianceScan()
      toasts.push({ kind: 'info', title: t('Scan finished'), detail: t('Score: {score}%', { score: res.score }) })
      await loadChecks()
    } catch (e) {
      toasts.push({ kind: 'error', title: t('Scan failed'), detail: errorText(e) })
    } finally {
      scanning = false
    }
  }

  // Export as one of the generated reports (Reports → Security compliance).
  let exportOpen = $state(false)
  let exportFormat = $state<ReportFormat>('html')
  let exporting = $state(false)
  async function exportReport() {
    exporting = true
    try {
      const m = await ent.generateReport('compliance', exportFormat)
      toasts.push({ kind: 'info', title: m.title, detail: m.summary })
      exportOpen = false
      if (m.format === 'html') window.open(ent.reportURL(m.id), '_blank', 'noopener')
    } catch (e) {
      toasts.push({ kind: 'error', title: t('Report failed'), detail: errorText(e) })
    } finally {
      exporting = false
    }
  }

  $effect(() => {
    void cluster.clustered
    void loadChecks()
  })

  // ---- Audit log integrity ---------------------------------------------------------------
  let audit = $state<AuditVerification | null>(null)
  let auditError = $state('')
  let auditDone = $state(false)

  async function loadAudit() {
    auditError = ''
    try {
      audit = await api.verifyAuditLog()
      auditDone = true
    } catch (e) {
      auditError = errorText(e)
    }
  }
  $effect(() => {
    if (tab === 'audit' && !auditDone && !auditError && session.can('operator')) void loadAudit()
  })

  // ---- FIPS ------------------------------------------------------------------------------
  let fips = $state<FIPSInfo | null>(null)
  let fipsError = $state('')
  let fipsDone = $state(false)
  async function loadFIPS() {
    fipsError = ''
    try {
      fips = await api.complianceFIPS()
      fipsDone = true
    } catch (e) {
      fipsError = errorText(e)
    }
  }
  $effect(() => {
    if (tab === 'fips' && !fipsDone && !fipsError) void loadFIPS()
  })

  const short = (h?: string) => (h ? `${h.slice(0, 14)}…${h.slice(-8)}` : '')
  const scoreClass = (score: number) => (score >= 90 ? 'ok' : score >= 70 ? 'warn' : 'bad')
  const RING = 2 * Math.PI * 52
</script>

<svelte:head><title>{t('Compliance · VirtoMir')}</title></svelte:head>

<header class="page-head">
  <div>
    <h1>{t('Compliance & Hardening')}</h1>
    <p class="lead">{t('Host CIS/STIG hardening checks, FIPS mode, and tamper-evident audit log integrity')}</p>
  </div>
  {#if session.can('operator')}
    <div class="tools">
      <button class="ui-btn" disabled={scanning || loading} onclick={runScan} title={session.why('operator')}>
        <RefreshCw size={15} class={scanning ? 'spin' : ''} /> {scanning ? t('Scanning…') : t('Run scan now')}
      </button>
      {#if session.can('admin')}
        <button class="ui-btn ghost" disabled={scanning} onclick={() => (exportOpen = true)} title={session.why('admin')}>
          <FileBarChart size={15} /> {t('Export report')}
        </button>
      {/if}
    </div>
  {/if}
</header>

<div class="tabs" role="radiogroup" aria-label={t('View')}>
  <button class="c-tab" class:active={tab === 'checks'} role="radio" aria-checked={tab === 'checks'} onclick={() => (tab = 'checks')}>
    {t('Hardening Checks')}
  </button>
  <button class="c-tab" class:active={tab === 'audit'} role="radio" aria-checked={tab === 'audit'} onclick={() => (tab = 'audit')}>
    {t('Audit Log Integrity')}
  </button>
  <button class="c-tab" class:active={tab === 'fips'} role="radio" aria-checked={tab === 'fips'} onclick={() => (tab = 'fips')}>
    {t('FIPS & Cryptography')}
  </button>
</div>

{#if error}
  <div class="ui-error-box">{error}</div>
{/if}

<!-- ===== 1 · Hardening checks ========================================================= -->
{#if tab === 'checks'}
  {#if cluster.clustered && comps.length > 1}
    <div class="hostpick ui-panel">
      <label class="ui-field inline">
        <span class="label">{t('Shown host')}</span>
        <select class="ui-input" bind:value={pickHost}>
          {#each comps as h (h.hostId)}
            <option value={h.hostId}>
              {h.hostName || h.hostId} — {h.score < 0 ? t('Not reachable') : `${h.score}%`}
            </option>
          {/each}
        </select>
      </label>
    </div>
  {/if}

  {#if loading && !current}
    <p class="muted">{t('Loading…')}</p>
  {:else if current}
    {#if cluster.clustered && current.score < 0}
      <div class="ui-panel card notice">
        <ShieldOff size={18} />
        <p>{t('This host cannot be reached right now; run a scan once it answers again.')}</p>
      </div>
    {:else}
      <section class="ui-panel deck">
        <div class="score">
          <svg viewBox="0 0 120 120" role="img" aria-label="{t('Score')}: {current.score}%">
            <circle class="ring-bg" cx="60" cy="60" r="52" />
            <circle
              class="ring-val {scoreClass(current.score)}"
              cx="60"
              cy="60"
              r="52"
              transform="rotate(-90 60 60)"
              stroke-dasharray="{(RING * current.score) / 100} {RING}"
            />
          </svg>
          <div class="score-text">
            <b class="mono {scoreClass(current.score)}">{current.score}%</b>
            <span>{t('Score')}</span>
          </div>
        </div>
        <dl class="counts">
          <div><dt>{t('Total')}</dt><dd class="mono">{current.totalCount}</dd></div>
          <div><dt>{t('Passed')}</dt><dd class="mono ok">{current.passedCount}</dd></div>
          <div><dt>{t('Failed')}</dt><dd class="mono bad">{current.failedCount}</dd></div>
          <div><dt>{t('Warnings')}</dt><dd class="mono oowie">{current.warningCount}</dd></div>
        </dl>
        <div class="when muted">
          <ShieldCheck size={14} />
          {t('Last evaluated: {when}', { when: new Date(current.evaluatedAt).toLocaleString() })}
        </div>
      </section>

      <div class="bar">
        <div class="pills" role="radiogroup" aria-label={t('Filter')}>
          <button class="pill" class:on={filter === 'all'} role="radio" aria-checked={filter === 'all'} onclick={() => (filter = 'all')}>{t('All')}</button>
          <button class="pill" class:on={filter === 'fail'} role="radio" aria-checked={filter === 'fail'} onclick={() => (filter = 'fail')}>{t('Failed')}</button>
          <button class="pill" class:on={filter === 'warn'} role="radio" aria-checked={filter === 'warn'} onclick={() => (filter = 'warn')}>{t('Warnings')}</button>
          <button class="pill" class:on={filter === 'pass'} role="radio" aria-checked={filter === 'pass'} onclick={() => (filter = 'pass')}>{t('Passed')}</button>
        </div>
        <input class="ui-input search" type="search" bind:value={query} placeholder={t('Search checks by title, category or ID')} spellcheck="false" aria-label={t('Search checks by title, category or ID')} />
      </div>

      <section class="ui-panel checks">
        {#if filtered.length === 0}
          <p class="muted pad">{t('No checks match the filters.')}</p>
        {:else}
          {#each filtered as c (c.id)}
            {@const st = STATUSES[c.status]}
            <div class="check">
              <button class="row" aria-expanded={openId === c.id} onclick={() => (openId = openId === c.id ? '' : c.id)}>
                <span class="caret"><ChevronDown size={14} /></span>
                <span class="id mono">{c.id}</span>
                <span class="title">{t(c.title)}</span>
                <span class="chip">{catLabel(c.category)}</span>
                <span class="sev sev-{c.severity}">{SEVERITIES[c.severity]}</span>
                <span class="st st-{c.status}"><st.icon size={14} /> {st.label}</span>
              </button>
              {#if openId === c.id}
                <div class="exp">
                  <p class="detail">{c.detail}</p>
                  {#if c.benchmark}
                    <p class="bench"><b>{t('Benchmark')}</b> <span class="mono">{c.benchmark}</span></p>
                  {/if}
                  {#if c.remediation}
                    <div class="rem">
                      <div class="rem-head">
                        <b>{t('Remediation')}</b>
                        <button class="ui-btn sm ghost" onclick={() => copy(c.remediation, c.id)}><Copy size={13} /> {t('Copy')}</button>
                      </div>
                      <pre class="mono">{c.remediation}</pre>
                    </div>
                  {/if}
                </div>
              {/if}
            </div>
          {/each}
        {/if}
      </section>
    {/if}
  {/if}
{/if}

<!-- ===== 2 · Audit log integrity ====================================================== -->
{#if tab === 'audit'}
  {#if !session.can('operator')}
    <div class="ui-error-box">{session.why('operator')}</div>
  {:else if auditError}
    <div class="ui-error-box">{auditError}</div>
  {:else if audit === null}
    <p class="muted">{t('Verifying…')}</p>
  {:else}
    <section class="ui-panel banner">
      <div class="banner-head">
        <span class="banner-icon" class:ok={audit.valid} class:bad={!audit.valid}>
          {#if audit.valid}<Lock size={20} />{:else}<ShieldAlert size={20} />{/if}
        </span>
        <div>
          <h2>{t('Tamper-Evident Audit Log')}</h2>
          <span class="badge {audit.valid ? 'ok' : 'bad'}">{audit.valid ? t('Chain Verified Intact') : t('Tampering Detected')}</span>
        </div>
        <div class="tools">
          <button class="ui-btn" onclick={loadAudit}><RefreshCw size={15} /> {t('Verify Log Integrity')}</button>
          {#if session.can('admin')}
            <a class="ui-btn" href={api.exportAuditLogURL('jsonl')} download title={session.why('admin')}><Download size={15} /> {t('JSONL')}</a>
            <a class="ui-btn" href={api.exportAuditLogURL('csv')} download title={session.why('admin')}><Download size={15} /> {t('CSV')}</a>
          {:else}
            <span class="muted small">{session.why('admin')}</span>
          {/if}
        </div>
      </div>
      {#if audit.valid}
        <dl class="meta">
          <div><dt>{t('Verified entries')}</dt><dd class="mono">{audit.verifiedCount}</dd></div>
          <div><dt>{t('Sequence range')}</dt><dd class="mono">#{audit.firstSeq ?? 1} – #{audit.lastSeq ?? audit.verifiedCount}</dd></div>
          <div>
            <dt>{t('Head hash (SHA-256)')}</dt>
            <dd class="mono head">
              {#if audit.headHash}
                {@const h = audit.headHash}
                {short(h)}
                <button class="ui-btn sm ghost" onclick={() => copy(h, t('Head hash (SHA-256)'))} title={t('Copy')}><Copy size={13} /></button>
              {:else}
                {t('Not recorded')}
              {/if}
            </dd>
          </div>
        </dl>
      {:else}
        <div class="ui-error-box tamper">
          {#if audit.tamperedSeq}
            {t('Entry #{seq} (file index {index}) no longer follows the chain: its hash does not build on the previous entry.', audit.tamperedIndex !== undefined ? { seq: audit.tamperedSeq, index: audit.tamperedIndex } : { seq: audit.tamperedSeq, index: audit.tamperedSeq - 1 })}
          {:else}
            {audit.reason || t('The audit log hash chain does not verify.')}
          {/if}
          {#if audit.reason}<p class="mono reason">{audit.reason}</p>{/if}
        </div>
      {/if}
    </section>
  {/if}
{/if}

<!-- ===== 3 · FIPS ===================================================================== -->
{#if tab === 'fips'}
  {#if fipsError}
    <div class="ui-error-box">{fipsError}</div>
  {:else if fips === null}
    <p class="muted">{t('Loading…')}</p>
  {:else}
    <section class="ui-panel fips">
      <div class="fips-head">
        <span class="banner-icon" class:ok={fips.enforced}><Lock size={20} /></span>
        <div>
          <h2>{t('Kernel FIPS mode')}</h2>
          <span class="badge" class:ok={fips.kernelEnabled} class:warn={!fips.kernelEnabled}>{fips.kernelEnabled ? t('Active') : t('Inactive')}</span>
          <span class="chip">{t('Experimental')}</span>
        </div>
      </div>
      <p class="detail">{fips.description}</p>
      <p class="hint muted">{t('Kernel mode is read from /proc/sys/crypto/fips_enabled; the daemon side restricts TLS on its own.')}</p>
    </section>

    <section class="ui-panel fips">
      <div class="fips-head">
        <span class="banner-icon" class:ok={fips.enforced}><ShieldCheck size={20} /></span>
        <div>
          <h2>{t('TLS Security Posture')}</h2>
          <span class="badge" class:ok={fips.enforced} class:warn={!fips.enforced}>{fips.enforced ? t('Enforced') : t('Not enforced')}</span>
        </div>
      </div>
      <p class="detail">{fips.enforced ? t('The daemon serves HTTPS only with the suites and curves below (minimum version enforced).') : t('The daemon uses Go defaults; no cipher or curve restrictions apply.')}</p>
      <div class="tls">
        <div class="tls-item">
          <span class="label">{t('Minimum TLS version')}</span>
          <span class="mono">{fips.tlsMinVersion || t('TLS 1.2')}</span>
        </div>
        <div class="tls-item">
          <span class="label">{t('Cipher suites')}</span>
          {#if fips.cipherSuites.length}
            <ul class="mono list">{#each fips.cipherSuites as s (s)}<li>{s}</li>{/each}</ul>
          {:else}
            <span class="muted">{t('All Go defaults')}</span>
          {/if}
        </div>
        <div class="tls-item">
          <span class="label">{t('Elliptic curves')}</span>
          {#if fips.curves.length}
            <span class="mono" title={fips.curves.join(', ')}>{fips.curves.join(', ')}</span>
          {:else}
            <span class="muted">{t('All Go defaults')}</span>
          {/if}
        </div>
      </div>
    </section>
  {/if}
{/if}

<Dialog open={exportOpen} title={t('Export report')} onclose={() => (exportOpen = false)} width={420}>
  <div class="form">
    <label class="ui-field">
      <span class="label">{t('Format')}</span>
      <select class="ui-input" bind:value={exportFormat}>
        <option value="html">{t('HTML (view, print to PDF)')}</option>
        <option value="csv">{t('CSV (spreadsheet)')}</option>
        <option value="json">{t('JSON (scripts)')}</option>
      </select>
    </label>
    <p class="hint">{t('The report covers the whole cluster and appears under Reports → Security compliance.')}</p>
  </div>
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={() => (exportOpen = false)}>{t('Cancel')}</button>
    <button class="ui-btn primary" disabled={exporting} onclick={exportReport}>{t('Make report')}</button>
  {/snippet}
</Dialog>

<style>
  .lead { margin: 0; color: var(--text-3); font-size: 13.5px; }
  .tools { display: flex; align-items: center; gap: 8px; }
  .spin { animation: spin 0.9s linear infinite; }
  @keyframes spin { to { transform: rotate(360deg); } }

  .tabs { display: flex; gap: 3px; margin-bottom: 18px; padding: 0 6px; overflow-x: auto; border-bottom: var(--frame) solid var(--ink); }
  .c-tab {
    flex: none; height: 34px; margin-bottom: -2px; padding: 0 13px;
    border: var(--frame) solid var(--ink); border-bottom: 0;
    background: var(--surface-2); box-shadow: var(--bevel);
    color: var(--text-2);
    font-family: var(--font-display); font-size: 15px; font-stretch: 80%; font-weight: 700;
    white-space: nowrap; cursor: pointer;
  }
  .c-tab:hover { color: var(--text); }
  .c-tab.active { position: relative; z-index: 1; height: 38px; margin-top: -4px; background: var(--invert-bg); box-shadow: none; color: var(--invert-fg); }
  .c-tab.active::after { content: ''; position: absolute; left: 0; right: 0; bottom: 0; height: 2px; background: var(--invert-bg); }

  .hostpick { margin-bottom: 14px; padding: 10px 14px; }
  .inline { max-width: 320px; }

  .deck { display: flex; flex-wrap: wrap; align-items: center; gap: 26px; padding: 18px 22px; margin-bottom: 14px; }
  .score { position: relative; flex: none; width: 96px; height: 96px; }
  .score svg { width: 100%; height: 100%; }
  .ring-bg { fill: none; stroke: var(--surface-hover); stroke-width: 9; }
  .ring-val { fill: none; stroke-width: 9; stroke-linecap: butt; }
  .ring-val.ok { stroke: var(--ok); }
  .ring-val.warn { stroke: var(--warn); }
  .ring-val.bad { stroke: var(--danger); }
  .score-text { position: absolute; inset: 0; display: grid; place-content: center; text-align: center; gap: 0; }
  .score-text b { font-size: 20px; }
  .score-text b.ok { color: var(--ok); }
  .score-text b.warn { color: var(--warn); }
  .score-text b.bad { color: var(--danger); }
  .score-text span { color: var(--text-3); font-size: 11px; }
  .counts { display: flex; gap: 26px; margin: 0; }
  .counts div { display: flex; flex-direction: column; align-items: center; gap: 2px; }
  .counts dt { color: var(--text-3); font-size: 11.5px; }
  .counts dd { margin: 0; font-size: 18px; font-weight: 700; }
  .mono.ok { color: var(--ok); }
  .mono.bad { color: var(--danger); }
  .mono.oowie { color: var(--warn); }
  .when { display: flex; align-items: center; gap: 6px; margin-left: auto; font-size: 12px; }

  .bar { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; margin-bottom: 10px; }
  .pills { display: flex; border: 1px solid var(--border-strong); background: var(--bg-elev); box-shadow: var(--bevel-in); }
  .pill { height: 26px; padding: 0 12px; border: 0; background: transparent; color: var(--text-2); font-family: var(--font-mono); font-size: 12px; font-weight: 600; cursor: pointer; }
  .pill:hover { color: var(--text); }
  .pill.on { background: var(--metal-accent); color: var(--accent-contrast); outline: 1px solid var(--border-strong); box-shadow: var(--bevel); }
  .search { max-width: 300px; margin-left: auto; }

  .checks { overflow: hidden; }
  .check { border-top: 1px solid var(--border); }
  .check:first-child { border-top: 0; }
  .row {
    display: flex; align-items: center; gap: 10px; width: 100%; padding: 9px 14px;
    border: 0; background: none; color: var(--text); text-align: left; cursor: pointer;
    font-size: 13px;
  }
  .row:hover { background: var(--surface-hover); }
  .caret { flex: none; color: var(--text-3); transition: transform var(--dur) var(--ease-out); }
  .row[aria-expanded='true'] .caret { transform: rotate(180deg); }
  .id { flex: none; max-width: 190px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--text-3); font-size: 11px; }
  .title { flex: 1; min-width: 140px; font-weight: 600; }
  .chip { flex: none; padding: 1px 8px; background: var(--surface-2); color: var(--text-2); font-size: 11px; font-weight: 600; }
  .sev { flex: none; min-width: 38px; text-align: center; padding: 1px 8px; font-size: 10.5px; font-weight: 700; border: 1px solid currentColor; }
  .sev-critical { color: var(--danger); background: var(--danger-soft); }
  .sev-high { color: var(--danger); }
  .sev-medium { color: var(--accent); }
  .sev-low { color: var(--text-3); }
  .st { flex: none; display: inline-flex; align-items: center; gap: 5px; padding: 1px 8px; font-size: 11.5px; font-weight: 700; border: 1px solid currentColor; }
  .st-pass { color: var(--ok); }
  .st-fail { color: var(--danger); background: var(--danger-soft); }
  .st-warn { color: var(--warn); }
  .st-error { color: var(--danger); }
  .exp { padding: 4px 14px 14px 38px; font-size: 13px; }
  .detail, .bench { margin: 6px 0; color: var(--text-2); }
  .bench b, .rem b { color: var(--text); font-size: 11.5px; text-transform: uppercase; letter-spacing: 0.04em; margin-right: 8px; }
  .rem { margin-top: 10px; }
  .rem-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: 4px; }
  .rem pre { margin: 0; padding: 10px 12px; border: var(--frame) solid var(--ink); background: var(--bg-elev); box-shadow: var(--bevel-in); font-size: 12.5px; white-space: pre-wrap; word-break: break-word; }
  .pad { padding: 14px; margin: 0; }

  .banner { padding: 18px 22px; margin-bottom: 14px; }
  .banner-head { display: flex; align-items: center; gap: 14px; flex-wrap: wrap; }
  .banner-head h2 { margin: 0 0 4px; font-size: 15px; }
  .banner-icon { display: grid; place-items: center; flex: none; width: 40px; height: 40px; border: var(--frame) solid var(--ink); background: var(--bg-elev); }
  .banner-icon.ok { color: var(--ok); }
  .banner-icon.bad { color: var(--danger); }
  .badge { display: inline-block; padding: 1px 10px; border: 1px solid currentColor; font-size: 11px; font-weight: 700; }
  .badge.ok { color: var(--ok); }
  .badge.bad { color: var(--danger); background: var(--danger-soft); }
  .badge.warn { color: var(--warn); }
  .banner-head .tools { margin-left: auto; }
  .meta { display: flex; flex-wrap: wrap; gap: 30px; margin: 16px 0 0; padding-top: 14px; border-top: 1px solid var(--border); }
  .meta div { display: flex; flex-direction: column; gap: 3px; }
  .meta dt { color: var(--text-3); font-size: 11.5px; }
  .meta dd { margin: 0; font-size: 13.5px; }
  .head { display: flex; align-items: center; gap: 8px; }
  .tamper p { margin: 8px 0 0; }
  .reason { font-size: 12px; color: var(--text-2); }

  .fips { padding: 18px 22px; margin-bottom: 14px; }
  .fips .banner-icon { border-radius: 0; }
  .fips .detail { margin: 10px 0 0; color: var(--text-2); }
  .fips .hint { font-size: 12px; margin: 6px 0 0; }
  .fips-head { display: flex; align-items: center; gap: 14px; }
  .fips-head h2 { margin: 0 0 4px; font-size: 15px; }
  .fips-head .chip { margin-left: 6px; }
  .tls { display: flex; flex-direction: column; gap: 10px; margin-top: 12px; }
  .tls-item { display: flex; gap: 14px; align-items: baseline; font-size: 13px; }
  .tls-item > .label { flex: none; width: 180px; color: var(--text-3); font-size: 11.5px; text-transform: uppercase; letter-spacing: 0.04em; }
  .list { margin: 0; padding: 0 0 0 18px; font-size: 12.5px; }
  .small { font-size: 11.5px; }

  .muted { color: var(--text-3); }
  @media (max-width: 720px) {
    .row { flex-wrap: wrap; }
    .title { flex-basis: 100%; }
    .when { margin-left: 0; }
  }
</style>
