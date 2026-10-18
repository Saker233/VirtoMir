import { describe, expect, it } from 'vitest'
import type { VMwarePreflightItem, VMwareVM, VMwareWaveItem } from './api'
import {
  aggregateWaveStates,
  distinctPortGroups,
  filterVMs,
  groupVMsByTree,
  hasHardErrors,
  isPreflightWarning,
} from './vmware'

describe('distinctPortGroups', () => {
  it('returns sorted unique non-empty port group names', () => {
    const vms: VMwareVM[] = [
      {
        id: 'vm-1',
        name: 'app-1',
        datacenter: 'DC1',
        host: 'esx-1',
        powerState: 'off',
        vcpus: 2,
        memoryMiB: 4096,
        guestOs: 'Linux',
        disks: [],
        nics: [
          { network: 'VM Network', mac: '00:50:56:01' },
          { network: 'prod-web', mac: '00:50:56:02' },
        ],
      },
      {
        id: 'vm-2',
        name: 'app-2',
        datacenter: 'DC1',
        host: 'esx-1',
        powerState: 'off',
        vcpus: 4,
        memoryMiB: 8192,
        guestOs: 'Linux',
        disks: [],
        nics: [
          { network: 'prod-web', mac: '00:50:56:03' },
          { network: 'backend-db', mac: '00:50:56:04' },
          { network: '', mac: '00:50:56:05' },
        ],
      },
    ]

    expect(distinctPortGroups(vms)).toEqual(['backend-db', 'prod-web', 'VM Network'])
  })

  it('handles empty VMs or missing nics', () => {
    expect(distinctPortGroups([])).toEqual([])
    expect(distinctPortGroups([{ id: 'vm-3', name: 'isolated', datacenter: 'DC1', host: 'esx-1', powerState: 'off', vcpus: 1, memoryMiB: 1024, guestOs: 'Linux', disks: [], nics: [] }])).toEqual([])
  })
})

describe('isPreflightWarning & hasHardErrors', () => {
  it('recognizes warnings correctly', () => {
    expect(isPreflightWarning('Warning: virtio-win ISO not specified')).toBe(true)
    expect(isPreflightWarning('warn: tools update recommended')).toBe(true)
    expect(isPreflightWarning('Non-critical issue (warning)')).toBe(true)
    expect(isPreflightWarning('The disk needs 50 GiB but only 20 GiB available')).toBe(false)
    expect(isPreflightWarning('Machine is powered on')).toBe(false)
  })

  it('detects hard errors in preflight results', () => {
    const warnOnly: VMwarePreflightItem[] = [
      { vmId: 'vm-1', issues: ['Warning: driver injection fallback'] },
      { vmId: 'vm-2', issues: [] },
    ]
    expect(hasHardErrors(warnOnly)).toBe(false)

    const withError: VMwarePreflightItem[] = [
      { vmId: 'vm-1', issues: ['Warning: driver injection fallback'] },
      { vmId: 'vm-2', issues: ['Insufficient storage pool capacity'] },
    ]
    expect(hasHardErrors(withError)).toBe(true)

    const recordFormat: Record<string, string[]> = {
      'vm-1': ['Warning: test warning'],
      'vm-2': ['Error: failed connection'],
    }
    expect(hasHardErrors(recordFormat)).toBe(true)

    const recordClean: Record<string, string[]> = {
      'vm-1': ['Warning: test warning'],
      'vm-2': [],
    }
    expect(hasHardErrors(recordClean)).toBe(false)
  })
})

describe('aggregateWaveStates', () => {
  it('counts states and determines finished / failure statuses', () => {
    const items: VMwareWaveItem[] = [
      { vmId: 'vm-1', name: 'app-1', state: 'done' },
      { vmId: 'vm-2', name: 'app-2', state: 'running', message: 'Converting disk 1' },
      { vmId: 'vm-3', name: 'app-3', state: 'pending' },
      { vmId: 'vm-4', name: 'app-4', state: 'failed', message: 'virt-v2v error' },
    ]

    const agg = aggregateWaveStates(items)
    expect(agg.total).toBe(4)
    expect(agg.done).toBe(1)
    expect(agg.running).toBe(1)
    expect(agg.pending).toBe(1)
    expect(agg.failed).toBe(1)
    expect(agg.finished).toBe(false)
    expect(agg.allDone).toBe(false)
    expect(agg.hasFailures).toBe(true)

    const allFinished: VMwareWaveItem[] = [
      { vmId: 'vm-1', name: 'app-1', state: 'done' },
      { vmId: 'vm-2', name: 'app-2', state: 'done' },
    ]
    const aggDone = aggregateWaveStates(allFinished)
    expect(aggDone.finished).toBe(true)
    expect(aggDone.allDone).toBe(true)
    expect(aggDone.hasFailures).toBe(false)
  })
})

describe('filterVMs', () => {
  const vms: VMwareVM[] = [
    { id: '1', name: 'web-prod-01', guestOs: 'Ubuntu Linux', host: 'esx-1.corp', datacenter: 'Frankfurt', powerState: 'off', vcpus: 2, memoryMiB: 4096, disks: [], nics: [] },
    { id: '2', name: 'db-prod-01', guestOs: 'RHEL 9', host: 'esx-2.corp', datacenter: 'Frankfurt', powerState: 'off', vcpus: 4, memoryMiB: 8192, disks: [], nics: [] },
    { id: '3', name: 'win-dc-01', guestOs: 'Windows Server 2022', host: 'esx-dub-1.corp', datacenter: 'Dublin', powerState: 'on', vcpus: 4, memoryMiB: 8192, disks: [], nics: [] },
  ]

  it('filters across name, OS, host, and datacenter', () => {
    expect(filterVMs(vms, '')).toHaveLength(3)
    expect(filterVMs(vms, 'web')).toEqual([vms[0]])
    expect(filterVMs(vms, 'rhel')).toEqual([vms[1]])
    expect(filterVMs(vms, 'dublin')).toEqual([vms[2]])
    expect(filterVMs(vms, 'esx-1')).toEqual([vms[0]])
    expect(filterVMs(vms, 'nonexistent')).toEqual([])
  })
})

describe('groupVMsByTree', () => {
  const vms: VMwareVM[] = [
    { id: '1', name: 'vm-b', guestOs: 'Linux', host: 'host-2', datacenter: 'dc-1', powerState: 'off', vcpus: 1, memoryMiB: 1024, disks: [], nics: [] },
    { id: '2', name: 'vm-a', guestOs: 'Linux', host: 'host-1', datacenter: 'dc-1', powerState: 'off', vcpus: 1, memoryMiB: 1024, disks: [], nics: [] },
    { id: '3', name: 'vm-c', guestOs: 'Linux', host: 'host-3', datacenter: 'dc-2', powerState: 'off', vcpus: 1, memoryMiB: 1024, disks: [], nics: [] },
  ]

  it('organizes VMs into sorted Datacenter -> Host hierarchy', () => {
    const tree = groupVMsByTree(vms)
    expect(tree).toHaveLength(2)
    expect(tree[0].name).toBe('dc-1')
    expect(tree[0].hosts).toHaveLength(2)
    expect(tree[0].hosts[0].name).toBe('host-1')
    expect(tree[0].hosts[0].vms[0].name).toBe('vm-a')
    expect(tree[1].name).toBe('dc-2')
    expect(tree[1].hosts[0].name).toBe('host-3')
  })
})
