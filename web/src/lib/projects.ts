// Project helpers (ADR-0060): pure functions for the Projects page, the machine list
// filter and the new-machine wizard. The store lives in projects.svelte.ts.

import type { Domain, Project, ProjectMember, ProjectUsage, Role } from './api'

export type { Project, ProjectMember, ProjectUsage }

/** Rank of a project role, like auth.Role ranks global ones ("viewer" < "operator" < "admin"). */
export const PROJECT_ROLE_RANK: Record<string, number> = { viewer: 1, operator: 2, admin: 3 }

/** The highest role the user has in the project: directly, or through one of their groups. */
export function roleFor(p: Project, user: string, groups: string[] = []): Role | undefined {
  let best: Role | undefined
  for (const m of p.members) {
    const hit = (m.kind === 'user' && m.name === user) || (m.kind === 'group' && groups.includes(m.name))
    const cur = best ? PROJECT_ROLE_RANK[best] : 0
    if (hit && PROJECT_ROLE_RANK[m.role] > cur) best = m.role
  }
  return best
}

/** Projects the user may change things in: at least an operator role in them. */
export function operableProjects(projects: Project[], user: string, groups: string[] = []): Project[] {
  return projects.filter((p) => {
    const r = roleFor(p, user, groups)
    return r === 'operator' || r === 'admin'
  })
}

/** What the given machines use together in one project (live numbers, no disk sizes). */
export function usageOf(domains: Pick<Domain, 'project' | 'vcpus' | 'maxMemoryKiB'>[], projectID: string): ProjectUsage {
  const u: ProjectUsage = { machines: 0, vcpus: 0, memoryMiB: 0, diskGiB: 0 }
  for (const d of domains) {
    if (d.project !== projectID) continue
    u.machines++
    u.vcpus += d.vcpus
    u.memoryMiB += Math.round(d.maxMemoryKiB / 1024)
  }
  return u
}

/** How much of one limit is in use, 0–100; null when there is no limit (0 = unlimited). */
export function usagePercent(used: number, limit: number): number | null {
  if (!limit) return null
  return Math.max(0, Math.min(100, Math.round((used / limit) * 100)))
}

/** The machines of one project, or the global ones when projectID is ''. */
export function machinesOf<T extends Pick<Domain, 'project'>>(domains: T[], projectID: string): T[] {
  return domains.filter((d) => (d.project ?? '') === projectID)
}