// Pure helpers and types for VMware migration wizard (ADR-0061).

import type { VMwarePreflightItem, VMwareVM, VMwareWaveItem } from './api'

export interface VMwareHostGroup {
  name: string
  vms: VMwareVM[]
}

export interface VMwareDatacenterGroup {
  name: string
  hosts: VMwareHostGroup[]
}

/** Extracts unique, sorted VMware network port groups among the provided VMs. */
export function distinctPortGroups(vms: VMwareVM[]): string[] {
  const seen = new Set<string>()
  for (const vm of vms) {
    for (const nic of vm.nics ?? []) {
      const net = nic.network?.trim()
      if (net) seen.add(net)
    }
  }
  return Array.from(seen).sort((a, b) => a.localeCompare(b))
}

/** Reports whether an issue string returned by preflight is an advisory warning rather than a hard failure. */
export function isPreflightWarning(issue: string): boolean {
  const lower = issue.trim().toLowerCase()
  return (
    lower.startsWith('warning') ||
    lower.startsWith('warn:') ||
    lower.includes('(warning)') ||
    lower.includes('warning:')
  )
}

/** Reports whether preflight found any blocking hard errors (non-warnings). */
export function hasHardErrors(issues: Record<string, string[]> | VMwarePreflightItem[] | string[]): boolean {
  if (Array.isArray(issues)) {
    for (const item of issues) {
      if (typeof item === 'string') {
        if (!isPreflightWarning(item)) return true
      } else {
        for (const iss of item.issues ?? []) {
          if (!isPreflightWarning(iss)) return true
        }
      }
    }
    return false
  }
  for (const list of Object.values(issues)) {
    for (const iss of list ?? []) {
      if (!isPreflightWarning(iss)) return true
    }
  }
  return false
}

/** Aggregates states across items of a migration wave. */
export function aggregateWaveStates(items: VMwareWaveItem[]): {
  total: number
  pending: number
  running: number
  done: number
  failed: number
  finished: boolean
  allDone: boolean
  hasFailures: boolean
} {
  let pending = 0
  let running = 0
  let done = 0
  let failed = 0
  for (const it of items) {
    switch (it.state) {
      case 'pending':
        pending++
        break
      case 'running':
        running++
        break
      case 'done':
        done++
        break
      case 'failed':
        failed++
        break
    }
  }
  const total = items.length
  const finished = total > 0 && done + failed === total
  const allDone = total > 0 && done === total
  const hasFailures = failed > 0
  return { total, pending, running, done, failed, finished, allDone, hasFailures }
}

/** Filters VMware VMs matching a search string by name, guest OS, host, or datacenter. */
export function filterVMs(vms: VMwareVM[], query: string): VMwareVM[] {
  const q = query.trim().toLowerCase()
  if (!q) return vms
  return vms.filter(
    (vm) =>
      vm.name?.toLowerCase().includes(q) ||
      vm.guestOs?.toLowerCase().includes(q) ||
      vm.host?.toLowerCase().includes(q) ||
      vm.datacenter?.toLowerCase().includes(q),
  )
}

/** Groups a list of VMs into a hierarchy of Datacenter → Host → VMs for tree display. */
export function groupVMsByTree(vms: VMwareVM[]): VMwareDatacenterGroup[] {
  const dcMap = new Map<string, Map<string, VMwareVM[]>>()
  for (const vm of vms) {
    const dcName = vm.datacenter?.trim() || 'Default Datacenter'
    const hostName = vm.host?.trim() || 'Standalone Host'
    if (!dcMap.has(dcName)) {
      dcMap.set(dcName, new Map())
    }
    const hostMap = dcMap.get(dcName)!
    if (!hostMap.has(hostName)) {
      hostMap.set(hostName, [])
    }
    hostMap.get(hostName)!.push(vm)
  }

  const result: VMwareDatacenterGroup[] = []
  for (const [dcName, hostMap] of Array.from(dcMap.entries()).sort(([a], [b]) => a.localeCompare(b))) {
    const hosts: VMwareHostGroup[] = []
    for (const [hostName, hostVMs] of Array.from(hostMap.entries()).sort(([a], [b]) => a.localeCompare(b))) {
      hosts.push({
        name: hostName,
        vms: hostVMs.slice().sort((a, b) => a.name.localeCompare(b.name)),
      })
    }
    result.push({ name: dcName, hosts })
  }
  return result
}
