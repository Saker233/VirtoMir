import { describe, expect, it } from 'vitest'
import { machinesOf, operableProjects, roleFor, usageOf, usagePercent } from './projects'
import type { Domain, Project } from './api'

const project = (over: Partial<Project>): Project => ({
  id: 'p',
  name: 'P',
  members: [],
  limits: { machines: 0, vcpus: 0, memoryMiB: 0, diskGiB: 0 },
  storagePools: [],
  networks: [],
  created: '',
  ...over,
})

const dom = (over: Partial<Domain>): Domain => ({
  uuid: Math.random().toString(16).slice(2),
  name: 'vm',
  state: 'shutoff',
  vcpus: 2,
  memoryKiB: 2048,
  maxMemoryKiB: 2048,
  cpuTimeNs: 0,
  persistent: true,
  autostart: false,
  cpuPercent: 0,
  ...over,
})

describe('usagePercent', () => {
  it('maps use onto 0–100 and leaves unlimited limits alone', () => {
    expect(usagePercent(3, 10)).toBe(30)
    expect(usagePercent(10, 10)).toBe(100)
    expect(usagePercent(12, 10)).toBe(100) // over the limit, never above the bar
    expect(usagePercent(0, 0)).toBeNull()
    expect(usagePercent(5, 0)).toBeNull()
  })
})

describe('roleFor', () => {
  const p = project({
    members: [
      { kind: 'user', name: 'alice', role: 'viewer' },
      { kind: 'group', name: 'ops', role: 'admin' },
    ],
  })
  it('takes the highest role: direct membership or through a group', () => {
    expect(roleFor(p, 'alice')).toBe('viewer')
    expect(roleFor(p, 'bob')).toBeUndefined()
    expect(roleFor(p, 'carol', ['ops'])).toBe('admin')
    expect(roleFor(p, 'alice', ['ops'])).toBe('admin')
  })
})

describe('operableProjects', () => {
  it('keeps the projects the user is an operator or admin in', () => {
    const ps = [
      project({ id: 'a', members: [{ kind: 'user', name: 'alice', role: 'viewer' }] }),
      project({ id: 'b', members: [{ kind: 'user', name: 'alice', role: 'operator' }] }),
      project({ id: 'c', members: [{ kind: 'group', name: 'ops', role: 'admin' }] }),
    ]
    expect(operableProjects(ps, 'alice')).toEqual([ps[1]])
    expect(operableProjects(ps, 'alice', ['ops'])).toEqual([ps[1], ps[2]])
  })
})

describe('usageOf', () => {
  it('adds up the machines of one project only', () => {
    const domains = [
      dom({ project: 'a', vcpus: 2, maxMemoryKiB: 4 * 1024 * 1024 }),
      dom({ project: 'a', vcpus: 4, maxMemoryKiB: 8 * 1024 * 1024 }),
      dom({ project: 'b', vcpus: 8 }),
      dom({}), // global
    ]
    expect(usageOf(domains, 'a')).toEqual({ machines: 2, vcpus: 6, memoryMiB: 12 * 1024, diskGiB: 0 })
  })
})

describe('machinesOf', () => {
  it('filters by project, with the empty project meaning the global machines', () => {
    const domains = [dom({ project: 'a' }), dom({}), dom({ project: 'b' })]
    expect(machinesOf(domains, 'a')).toHaveLength(1)
    expect(machinesOf(domains, '')).toHaveLength(1)
    expect(machinesOf(domains, 'b')[0].project).toBe('b')
  })
})