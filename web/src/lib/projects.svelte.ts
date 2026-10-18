// The project list, shared by the Projects page, the machine list filter, the machine
// page and the new-machine wizard (ADR-0060). Loaded once, reloaded by each page that
// changes it.

import { api, type Project } from './api'
import { operableProjects } from './projects'

class ProjectsStore {
  list = $state<Project[]>([])
  loaded = $state(false)

  async load() {
    try {
      this.list = await api.projects()
    } catch {
      this.list = []
    }
    this.loaded = true
  }

  /** The project's name, or its ID when it is not in the list (or ""). */
  name(id: string): string {
    if (!id) return ''
    return this.list.find((p) => p.id === id)?.name ?? id
  }

  /** Projects the signed-in user may change things in (operator role or better). */
  operable(user: string | undefined, groups: string[] = []): Project[] {
    if (!user) return []
    return operableProjects(this.list, user, groups)
  }
}

export const projects = new ProjectsStore()