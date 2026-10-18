<script lang="ts">
  import { t } from '../i18n.svelte'
  import { api, errorText, type Pool, type Project, type ProjectMember, type VNetwork } from '../api'
  import { projects as store } from '../projects.svelte'
  import { usageOf, usagePercent } from '../projects'
  import { live } from '../live.svelte'
  import { session } from '../session.svelte'
  import { formatKiB } from '../format'
  import { toasts } from '../toast.svelte'
  import Dialog from '../components/Dialog.svelte'
  import ConfirmButton from '../components/ConfirmButton.svelte'
  import FolderKanban from '@lucide/svelte/icons/folder-kanban'
  import Plus from '@lucide/svelte/icons/plus'
  import Pencil from '@lucide/svelte/icons/pencil'
  import Trash from '@lucide/svelte/icons/trash'
  import UserRound from '@lucide/svelte/icons/user-round'
  import UsersRound from '@lucide/svelte/icons/users-round'

  const ROLES: ProjectMember['role'][] = ['viewer', 'operator', 'admin']

  let list = $derived(store.list)
  let pools = $state<Pool[]>([])
  let networks = $state<VNetwork[]>([])
  let domains = $derived(live.snapshot?.domains ?? [])
  let loaded = $derived(store.loaded)

  let editOpen = $state(false)
  /** The project being edited; id '' = creating a new one (never null while the dialog is open). */
  let editing = $state<Project>(blank())
  let busy = $state(false)
  let error = $state('')

  async function load() {
    await store.load()
    try {
      ;[pools, networks] = await Promise.all([api.pools(), api.networks()])
    } catch {
      /* the allowed lists stay empty; the names can still be typed in */
    }
  }
  $effect(() => {
    if (session.can('admin')) void load()
  })

  function blank(): Project {
    return {
      id: '',
      name: '',
      description: '',
      members: [],
      limits: { machines: 0, vcpus: 0, memoryMiB: 0, diskGiB: 0 },
      storagePools: [],
      networks: [],
      created: '',
    }
  }

  function openEdit(p: Project | null) {
    editing = p ? { ...p, members: p.members.map((m) => ({ ...m })) } : blank()
    error = ''
    editOpen = true
  }

  function addMember() {
    editing?.members.push({ kind: 'user', name: '', role: 'operator' })
  }
  function delMember(i: number) {
    editing?.members.splice(i, 1)
  }

  function toggle(list: string[], name: string): string[] {
    return list.includes(name) ? list.filter((n) => n !== name) : [...list, name]
  }

  async function save() {
    const p = editing
    busy = true
    error = ''
    try {
      if (p.id && list.some((x) => x.id === p.id)) await api.updateProject(p.id, p)
      else await api.createProject(p)
      toasts.push({ kind: 'info', title: t('{name} saved', { name: p.name }) })
      editOpen = false
      await load()
    } catch (e) {
      error = errorText(e)
    } finally {
      busy = false
    }
  }

  async function remove(p: Project) {
    try {
      await api.deleteProject(p.id)
      toasts.push({ kind: 'info', title: t('{name} deleted', { name: p.name }) })
      await load()
    } catch (e) {
      toasts.push({ kind: 'error', title: t('Not deleted'), detail: errorText(e) })
    }
  }

  function usage(p: Project) {
    return usageOf(domains, p.id)
  }

  /** One line of "used / limit" with a bar; empty limit shows just the use. */
  function bar(p: Project, key: 'machines' | 'vcpus' | 'memoryMiB' | 'diskGiB'): { text: string; pct: number | null } {
    const u = usage(p)[key]
    const l = p.limits[key]
    const fmt = (n: number) => (key === 'memoryMiB' ? formatKiB(n * 1024) : key === 'diskGiB' ? `${n} GiB` : String(n))
    return { text: l ? t('{used} of {limit}', { used: fmt(u), limit: fmt(l) }) : fmt(u), pct: usagePercent(u, l) }
  }

  const BAR_KEYS: ('machines' | 'vcpus' | 'memoryMiB' | 'diskGiB')[] = ['machines', 'vcpus', 'memoryMiB', 'diskGiB']
  let rows = $derived(
    list.map((p) => ({ p, bars: BAR_KEYS.map((key) => ({ key, ...bar(p, key) })) })),
  )
</script>

<svelte:head><title>{t('Projects · VirtoMir')}</title></svelte:head>

{#if !session.can('admin')}
  <div class="ui-error-box" role="alert">{session.why('admin')}</div>
{:else}
<header class="page-head">
  <div>
    <div class="title-row">
      <FolderKanban size={22} />
      <h1>{t('Projects')}</h1>
    </div>
    <p class="lead">{t('Machines, pools and networks grouped per team, with members and limits.')}</p>
  </div>
  <button class="ui-btn primary" onclick={() => openEdit(null)}><Plus size={15} /> {t('New project')}</button>
</header>

<section class="ui-panel">
  {#if !loaded}
    <div class="empty">{t('Loading…')}</div>
  {:else if list.length === 0}
    <div class="empty">
      {t('No projects yet.')}
      <button class="link" onclick={() => openEdit(null)}>{t('Create the first one')}</button>
    </div>
  {:else}
    <div class="table" role="table" aria-label={t('Projects')}>
      <div class="colhead" role="row">
        <span role="columnheader">{t('Name')}</span>
        <span role="columnheader">{t('Members')}</span>
        <span role="columnheader">{t('Machines')}</span>
        <span role="columnheader">{t('vCPUs')}</span>
        <span role="columnheader">{t('Memory')}</span>
        <span role="columnheader">{t('Disk')}</span>
        <span role="columnheader" class="sr">{t('Actions')}</span>
      </div>
      {#each rows as r (r.p.id)}
        <div class="row" role="row">
          <div role="cell">
            <span class="name">{r.p.name}</span>
            {#if r.p.description}<span class="desc">{r.p.description}</span>{/if}
          </div>
          <div role="cell">
            {#each r.p.members as m (m.kind + m.name)}
              <span class="member" class:group={m.kind === 'group'} title={m.kind === 'group' ? t('Everyone in the group {name}', { name: m.name }) : m.name}>
                {#if m.kind === 'group'}<UsersRound size={12} />{:else}<UserRound size={12} />{/if}
                {m.name}<small>{m.role}</small>
              </span>
            {:else}
              <span class="none">{t('none')}</span>
            {/each}
          </div>
          {#each r.bars as b (b.key)}
            <div role="cell" class="use">
              <span class="num">{b.text}</span>
              {#if b.pct !== null}
                <span class="meter"><span class="fill" class:hot={b.pct >= 90} style:width="{b.pct}%"></span></span>
              {/if}
            </div>
          {/each}
          <div role="cell" class="actions">
            <button class="ui-btn sm" title={t('Edit {name}', { name: r.p.name })} onclick={() => openEdit(r.p)}><Pencil size={14} /> {t('Edit')}</button>
            <ConfirmButton label={t('Delete')} confirm={t('Delete {name}?', { name: r.p.name })} icon={Trash} failure={t('Could not delete {name}', { name: r.p.name })} run={() => remove(r.p)} />
          </div>
        </div>
      {/each}
    </div>
  {/if}
</section>

<Dialog open={editOpen} title={editing?.id ? t('Edit {name}', { name: editing?.name }) : t('New project')} width={620} onclose={() => (editOpen = false)}>
  {#if editing}
    <div class="form">
      {#if !list.some((p) => p.id === editing.id)}
        <label class="ui-field">
          <span class="label">{t('ID')}</span>
          <input class="ui-input mono" bind:value={editing.id} placeholder="team-a" spellcheck="false" />
          <span class="hint">{t('Lower-case letters, digits and dashes; used in API paths.')}</span>
        </label>
      {/if}
      <label class="ui-field">
        <span class="label">{t('Name')}</span>
        <input class="ui-input" bind:value={editing.name} />
      </label>
      <label class="ui-field">
        <span class="label">{t('Description')}</span>
        <input class="ui-input" bind:value={editing.description} />
      </label>

      <fieldset>
        <legend>{t('Members')}</legend>
        {#each editing.members as m, i (i)}
          <div class="member-row">
            <select class="ui-input" bind:value={m.kind} aria-label={t('Member kind')}>
              <option value="user">{t('User')}</option>
              <option value="group">{t('Group')}</option>
            </select>
            <input class="ui-input mono" bind:value={m.name} placeholder={m.kind === 'group' ? 'ml-team' : 'deniz'} aria-label={t('Member name')} spellcheck="false" />
            <select class="ui-input" bind:value={m.role} aria-label={t('Role')}>
              {#each ROLES as r (r)}<option value={r}>{r}</option>{/each}
            </select>
            <button class="ui-btn sm ghost" onclick={() => delMember(i)} title={t('Remove this member')} aria-label={t('Remove this member')}><Trash size={14} /></button>
          </div>
        {/each}
        <button type="button" class="ui-btn sm" onclick={addMember}><Plus size={14} /> {t('Add member')}</button>
      </fieldset>

      <fieldset>
        <legend>{t('Limits')} <small>{t('0 = unlimited')}</small></legend>
        <div class="limits">
          <label class="ui-field"><span class="label">{t('Machines')}</span><input class="ui-input num" type="number" min="0" bind:value={editing.limits.machines} /></label>
          <label class="ui-field"><span class="label">{t('vCPUs')}</span><input class="ui-input num" type="number" min="0" bind:value={editing.limits.vcpus} /></label>
          <label class="ui-field"><span class="label">{t('Memory (MiB)')}</span><input class="ui-input num" type="number" min="0" bind:value={editing.limits.memoryMiB} /></label>
          <label class="ui-field"><span class="label">{t('Disk (GiB)')}</span><input class="ui-input num" type="number" min="0" bind:value={editing.limits.diskGiB} /></label>
        </div>
      </fieldset>

      <fieldset>
        <legend>{t('Allowed storage pools')}</legend>
        <div class="checks">
          {#each pools as p (p.name)}
            <label class="check"><input type="checkbox" checked={editing.storagePools.includes(p.name)} onchange={() => (editing.storagePools = toggle(editing.storagePools, p.name))} /> {p.name}</label>
          {:else}
            <span class="hint">{t('No storage pools on this host.')}</span>
          {/each}
        </div>
      </fieldset>
      <fieldset>
        <legend>{t('Allowed networks')}</legend>
        <div class="checks">
          {#each networks as n (n.name)}
            <label class="check"><input type="checkbox" checked={editing.networks.includes(n.name)} onchange={() => (editing.networks = toggle(editing.networks, n.name))} /> {n.name}</label>
          {:else}
            <span class="hint">{t('No networks on this host.')}</span>
          {/each}
        </div>
      </fieldset>

      {#if error}<div class="ui-error-box">{error}</div>{/if}
      <div class="btns">
        <button class="ui-btn primary" disabled={busy || !editing.id || !editing.name} onclick={() => void save()}>
          {busy ? t('Saving…') : t('Save')}
        </button>
      </div>
    </div>
  {/if}
  </Dialog>
{/if}

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
  .table {
    display: grid;
  }
  .colhead,
  .row {
    display: grid;
    grid-template-columns: minmax(160px, 1.4fr) minmax(140px, 1.2fr) repeat(4, minmax(90px, 0.8fr)) 160px;
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
  .name {
    font-weight: 600;
    color: var(--text);
  }
  .desc {
    display: block;
    color: var(--text-3);
    font-size: 12px;
  }
  .member {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    margin: 0 6px 4px 0;
    padding: 1px 6px;
    border: 1px solid var(--border-strong);
    background: var(--surface-2);
    font-family: var(--font-mono);
    font-size: 11.5px;
  }
  .member small {
    color: var(--text-3);
    font-size: 10px;
  }
  .member.group {
    color: var(--accent);
  }
  .none {
    color: var(--text-3);
    font-size: 12px;
  }
  .use {
    display: flex;
    flex-direction: column;
    gap: 4px;
    font-size: 12.5px;
    color: var(--text-2);
  }
  .meter {
    width: 100%;
    height: 6px;
    border: 1px solid var(--border-strong);
    background: var(--surface-2);
  }
  .fill {
    display: block;
    height: 100%;
    background: var(--ok);
  }
  .fill.hot {
    background: var(--danger);
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
  .member-row {
    display: grid;
    grid-template-columns: 90px 1fr 100px 30px;
    gap: 6px;
    align-items: center;
  }
  .limits {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(110px, 1fr));
    gap: 8px;
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
  .btns {
    display: flex;
    justify-content: flex-end;
  }
  .hint {
    color: var(--text-3);
    font-size: 12px;
  }
  @media (max-width: 900px) {
    .colhead,
    .row {
      grid-template-columns: 1fr 1fr;
    }
    .colhead > :nth-child(3),
    .row > :nth-child(3),
    .colhead > :nth-child(4),
    .row > :nth-child(4),
    .colhead > :nth-child(5),
    .row > :nth-child(5),
    .colhead > :nth-child(6),
    .row > :nth-child(6) {
      display: none;
    }
  }
</style>