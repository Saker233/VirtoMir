import { describe, expect, it, vi } from 'vitest'
import { handle } from './routes'

const call = (method: string, path: string, body?: unknown) => {
  const r = handle(method, new URL('http://demo' + path), body)
  return { status: r.status, json: r.body && r.type?.startsWith('application/json') ? JSON.parse(r.body) : r.body }
}

const WEB1 = '4f1c2a7e-9b3d-4c21-8e5f-0a1b2c3d4e01'
const DB1 = '4f1c2a7e-9b3d-4c21-8e5f-0a1b2c3d4e03'
const CI = '4f1c2a7e-9b3d-4c21-8e5f-0a1b2c3d4e06'

describe('demo API', () => {
  it('answers the pages the UI opens first', () => {
    for (const p of ['/api/v1/auth/session', '/api/v1/cluster', '/api/v1/domains', '/api/v1/storage/pools', '/api/v1/networks', '/api/v1/alerts', '/api/v1/ha/status', '/api/v1/projects']) {
      expect(call('GET', p).status, p).toBe(200)
    }
    expect(call('GET', `/api/v1/domains/${DB1}`).json.name).toBe('db-01')
    expect(call('GET', `/api/v1/domains/${DB1}/xml`).json).toContain('<name>db-01</name>')
    expect(call('GET', `/api/v1/domains/${WEB1}`).status).toBe(404)
    expect(call('GET', `/api/v1/hosts/b2c3d4e5f6071829/domains/${WEB1}`).json.name).toBe('web-01')
  })

  it('serves projects and moves machines into them', () => {
    expect(call('GET', '/api/v1/projects').json.projects).toHaveLength(2)
    expect(call('GET', '/api/v1/projects/prod').json.name).toBe('Production')
    expect(call('GET', '/api/v1/projects/nope').status).toBe(404)

    expect(call('PUT', `/api/v1/domains/${CI}/project`, { project: 'ml' }).status).toBe(204)
    expect(call('GET', `/api/v1/domains/${CI}`).json.project).toBe('ml')

    const created = call('POST', '/api/v1/projects', { id: 'test', name: 'Test', members: [], limits: { machines: 1, vcpus: 1, memoryMiB: 0, diskGiB: 0 }, storagePools: [], networks: [] })
    expect(created.status).toBe(201)
    expect(call('PUT', '/api/v1/projects/test', { ...created.json.project, name: 'Test 2' }).json.project.name).toBe('Test 2')
    expect(call('DELETE', '/api/v1/projects/test').status).toBe(204)
    expect(call('GET', '/api/v1/projects/test').status).toBe(404)
  })

  it('scopes /hosts/<id>/ to that member', () => {
    const own = call('GET', '/api/v1/domains').json.domains.map((d: { name: string }) => d.name)
    const other = call('GET', '/api/v1/hosts/b2c3d4e5f6071829/domains').json.domains.map((d: { name: string }) => d.name)
    expect(own).toContain('db-01')
    expect(other).toContain('k8s-cp-01')
    expect(other).toContain('web-01')
    expect(other).not.toContain('db-01')
    expect(call('GET', '/api/v1/hosts/0000000000000000/domains').status).toBe(404)
  })

  it('serves placement rules, labels, violations and check', () => {
    const p = call('GET', '/api/v1/placement').json
    expect(p.rules.length).toBeGreaterThanOrEqual(3)
    expect(p.hostLabels['a1b2c3d4e5f60718']).toEqual({ rack: 'r1', zone: 'zone-a' })

    const viols = call('GET', '/api/v1/placement/violations').json
    expect(viols.some((v: any) => v.hard && v.kind === 'vm_anti_affinity')).toBe(true)

    const checkHard = call('POST', '/api/v1/placement/check', { domainUuid: WEB1, hostId: 'b2c3d4e5f6071829' }).json
    expect(checkHard.ok).toBe(false)
    expect(checkHard.hard.length).toBeGreaterThan(0)

    const checkClean = call('POST', '/api/v1/placement/check', { domainUuid: WEB1, hostId: 'c3d4e5f60718293a' }).json
    expect(checkClean.ok).toBe(true)

    // Rule CRUD
    const newRule = call('POST', '/api/v1/placement/rules', {
      name: 'Test Anti',
      kind: 'vm_anti_affinity',
      strength: 'must',
      domains: [WEB1, CI],
    })
    expect(newRule.status).toBe(201)
    const ruleId = newRule.json.id

    const updated = call('PUT', `/api/v1/placement/rules/${ruleId}`, {
      ...newRule.json,
      name: 'Test Anti Updated',
    })
    expect(updated.status).toBe(200)
    expect(updated.json.name).toBe('Test Anti Updated')

    expect(call('DELETE', `/api/v1/placement/rules/${ruleId}`).status).toBe(204)

    // Host labels
    const setLbl = call('PUT', '/api/v1/placement/hosts/a1b2c3d4e5f60718/labels', {
      labels: { rack: 'r1', row: 'row-3' },
    })
    expect(setLbl.status).toBe(200)
    expect(setLbl.json.labels.row).toBe('row-3')

    // DRS rules consistency
    const drs = call('GET', '/api/v1/cluster/drs').json
    expect(drs.rules.length).toBeGreaterThanOrEqual(3)
    expect(drs.rules.some((r: any) => r.name === 'Separate web frontends')).toBe(true)

    // Inventory machines endpoint contract
    const inv = call('GET', '/api/v1/cluster/inventory/machines').json
    expect(Array.isArray(inv.domains)).toBe(true)
    expect(inv.domains.length).toBeGreaterThanOrEqual(3)
  })

  it('applies power actions after a moment', () => {
    vi.useFakeTimers()
    expect(call('POST', `/api/v1/domains/${CI}/actions/start`).status).toBe(204)
    vi.advanceTimersByTime(2000)
    expect(call('GET', `/api/v1/domains/${CI}`).json.state).toBe('running')
    vi.useRealTimers()
  })

  it('serves history in the requested resolution', () => {
    const r = call('GET', `/api/v1/history?range=24h&series=host.cpu,vm.${WEB1}.cpu`).json
    expect(r.step).toBe(60)
    expect(r.series['host.cpu']).toHaveLength(1440)
    expect(Math.max(...r.series['host.cpu'])).toBeLessThanOrEqual(100)
    expect(call('GET', '/api/v1/history?range=2y&series=host.cpu').status).toBe(400)
  })

  it('serves VMware migration demo endpoints', () => {
    // 1. Connection
    const conn = call('GET', '/api/v1/vmware/connection').json
    expect(conn.url).toBe('https://vcenter.demo.internal')
    expect(conn.password).toBe('••••••••')

    const testRes = call('POST', '/api/v1/vmware/connection/test', {}).json
    expect(testRes.ok).toBe(true)

    // 2. Inventory
    const inv = call('GET', '/api/v1/vmware/inventory').json
    expect(inv.datacenters).toHaveLength(2)
    expect(inv.hosts).toHaveLength(2)
    expect(inv.vms.length).toBeGreaterThanOrEqual(4)
    expect(inv.vms.some((v: { powerState: string }) => v.powerState === 'off')).toBe(true)
    expect(inv.vms.some((v: { powerState: string }) => v.powerState === 'on')).toBe(true)

    // 3. Preflight
    const pre = call('POST', '/api/v1/vmware/preflight', { vmIds: ['vm-101', 'vm-104'], pool: 'default' }).json
    expect(pre.pool).toBe('default')
    expect(pre.issues['vm-101']).toHaveLength(0)
    expect(pre.issues['vm-104'][0]).toContain('Warning:')

    // 4. Wave import
    const waveJob = call('POST', '/api/v1/vmware/waves', { vmIds: ['vm-101', 'vm-102'], pool: 'default', parallel: 2 })
    expect(waveJob.status).toBe(202)
    const wavesList = call('GET', '/api/v1/vmware/waves').json.waves
    expect(wavesList.length).toBeGreaterThanOrEqual(1)
    const firstWave = wavesList[0]
    expect(call('GET', `/api/v1/vmware/waves/${firstWave.id}`).status).toBe(200)

    // 5. Single import
    const singleJob = call('POST', '/api/v1/vmware/import', { vmId: 'vm-101', pool: 'default', name: 'demo-imported-vm' })
    expect(singleJob.status).toBe(202)
  })

  it('validates storage pool during preflight and handles wave failures with retry', () => {
    // Preflight pool validations
    const emptyPoolRes = call('POST', '/api/v1/vmware/preflight', { vmIds: ['vm-101'], pool: '' })
    expect(emptyPoolRes.status).toBe(422)

    const unknownPoolRes = call('POST', '/api/v1/vmware/preflight', { vmIds: ['vm-101'], pool: 'non-existent-pool' })
    expect(unknownPoolRes.status).toBe(422)
    expect(unknownPoolRes.json.error).toContain('non-existent-pool')

    const validPre = call('POST', '/api/v1/vmware/preflight', { vmIds: ['vm-101'], pool: 'default' })
    expect(validPre.status).toBe(200)
    expect(validPre.json.pool).toBe('default')
    expect(validPre.json.poolFreeBytes).toBeGreaterThan(0)
    expect(validPre.json.poolPath).toBe('/var/lib/libvirt/images')

    // Wave partial failure and retry workflow
    vi.useFakeTimers()
    const waveRes = call('POST', '/api/v1/vmware/waves', { vmIds: ['vm-101', 'vm-104'], pool: 'default' })
    expect(waveRes.status).toBe(202)
    const waveList = call('GET', '/api/v1/vmware/waves').json.waves
    const wave = waveList[0]
    expect(wave).toBeDefined()

    // Advance timers so wave migration completes
    vi.advanceTimersByTime(4000)

    const finishedWave = call('GET', `/api/v1/vmware/waves/${wave.id}`).json
    const vm101 = finishedWave.items.find((it: { vmId: string }) => it.vmId === 'vm-101')
    const vm104 = finishedWave.items.find((it: { vmId: string }) => it.vmId === 'vm-104')
    expect(vm101?.state).toBe('done')
    expect(vm104?.state).toBe('failed')
    expect(vm104?.message).toContain('VirtIO')

    // Retry the failed item in the wave
    const retryRes = call('POST', `/api/v1/vmware/waves/${wave.id}/retry`, {})
    expect(retryRes.status).toBe(202)

    // Items being retried transition to running
    const retryingWave = call('GET', `/api/v1/vmware/waves/${wave.id}`).json
    const vm104Retrying = retryingWave.items.find((it: { vmId: string }) => it.vmId === 'vm-104')
    expect(vm104Retrying?.state).toBe('running')

    // Advance timer so retry completes
    vi.advanceTimersByTime(3500)

    const completedWave = call('GET', `/api/v1/vmware/waves/${wave.id}`).json
    const vm104Done = completedWave.items.find((it: { vmId: string }) => it.vmId === 'vm-104')
    expect(vm104Done?.state).toBe('done')
    expect(completedWave.items.every((it: { state: string }) => it.state === 'done')).toBe(true)

    // Check machine was added
    const domains = call('GET', '/api/v1/domains').json.domains
    expect(domains.some((d: { name: string }) => d.name === 'win-dc-01')).toBe(true)

    vi.useRealTimers()
  })

  it('manages content library items, versions, config, sync, and deploy', () => {
    // 1. List items
    const listRes = call('GET', '/api/v1/content/items')
    expect(listRes.status).toBe(200)
    expect(listRes.json.items.length).toBeGreaterThanOrEqual(3)
    const u2404 = listRes.json.items.find((it: { id: string }) => it.id === 'ci-ubuntu-2404')
    expect(u2404).toBeDefined()
    expect(u2404.type).toBe('image')
    expect(u2404.syncStatus).toBe('ready')
    expect(u2404.latestVersion.version).toBe('24.04.1')

    // 2. Publish new item
    const publishRes = call('POST', '/api/v1/content/items', {
      name: 'Alpine 3.20 Standard',
      type: 'iso',
      description: 'Lightweight security-oriented Linux',
      version: '3.20.0',
      keepVersions: 3,
      syncPolicy: { mode: 'all' },
    })
    expect(publishRes.status).toBe(201)
    const newItem = publishRes.json.item
    expect(newItem.name).toBe('Alpine 3.20 Standard')
    expect(newItem.versions).toHaveLength(1)

    // 3. Get item by id
    const getRes = call('GET', `/api/v1/content/items/${newItem.id}`)
    expect(getRes.status).toBe(200)
    expect(getRes.json.name).toBe('Alpine 3.20 Standard')

    // 4. Update item
    const updateRes = call('PUT', `/api/v1/content/items/${newItem.id}`, {
      description: 'Updated description for Alpine',
    })
    expect(updateRes.status).toBe(200)
    expect(updateRes.json.item.description).toBe('Updated description for Alpine')

    // 5. Publish new version
    const verRes = call('POST', `/api/v1/content/items/${newItem.id}/versions`, {
      version: '3.20.1',
      description: 'Point release with bug fixes',
    })
    expect(verRes.status).toBe(201)
    expect(verRes.json.version.version).toBe('3.20.1')
    expect(verRes.json.item.versions).toHaveLength(2)

    // 6. Delete a version
    const delVerRes = call('DELETE', `/api/v1/content/items/${newItem.id}/versions/${verRes.json.version.id}`)
    expect(delVerRes.status).toBe(204)
    const itemAfterVerDel = call('GET', `/api/v1/content/items/${newItem.id}`).json
    expect(itemAfterVerDel.versions).toHaveLength(1)

    // 7. Sync content
    const syncRes = call('POST', `/api/v1/content/items/${newItem.id}/sync`, {})
    expect(syncRes.status).toBe(202)
    expect(syncRes.json.kind).toBe('content-sync')

    // 8. Deploy from content
    vi.useFakeTimers()
    const deployRes = call('POST', `/api/v1/content/items/${newItem.id}/deploy`, {
      name: 'alpine-vm-01',
      pool: 'default',
      vcpus: 1,
      memoryMiB: 1024,
      start: true,
    })
    expect(deployRes.status).toBe(202)
    expect(deployRes.json.kind).toBe('content-deploy')

    // Advance timer so deployment completes
    vi.advanceTimersByTime(4000)
    const domains = call('GET', '/api/v1/domains').json.domains
    expect(domains.some((d: { name: string }) => d.name === 'alpine-vm-01')).toBe(true)
    vi.useRealTimers()

    // 9. Delete item
    const delRes = call('DELETE', `/api/v1/content/items/${newItem.id}`)
    expect(delRes.status).toBe(204)
    expect(call('GET', `/api/v1/content/items/${newItem.id}`).status).toBe(404)

    // 10. Get and Update Config
    const cfgRes = call('GET', '/api/v1/content/config')
    expect(cfgRes.status).toBe(200)
    expect(cfgRes.json.targetPool).toBe('default')

    const saveCfgRes = call('PUT', '/api/v1/content/config', {
      targetPool: 'fast-ssd',
      autoSync: false,
    })
    expect(saveCfgRes.status).toBe(200)
    expect(saveCfgRes.json.targetPool).toBe('fast-ssd')
    expect(saveCfgRes.json.autoSync).toBe(false)
  })
})
