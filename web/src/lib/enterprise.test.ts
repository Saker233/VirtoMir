import { describe, expect, it, vi } from 'vitest'

// Set up browser globals before dynamically importing modules that instantiate router
;(globalThis as any).window = { addEventListener: () => {} }
;(globalThis as any).location = { pathname: '/', hash: '', search: '', origin: 'http://localhost' }

const { rankEvacuationCandidates, violationsByDomain, ent } = await import('./enterprise')
const { api } = await import('./api')
type EvacuationCandidate = import('./enterprise').EvacuationCandidate
type PlacementViolation = import('./enterprise').PlacementViolation

describe('enterprise placement & evacuation helpers', () => {
  describe('violationsByDomain', () => {
    it('returns empty record for empty list', () => {
      expect(violationsByDomain([])).toEqual({})
    })

    it('maps domain uuids to violation', () => {
      const v1: PlacementViolation = {
        ruleId: 'r1',
        ruleName: 'Rule 1',
        kind: 'vm_anti_affinity',
        hard: false,
        domainUuids: ['vm-1', 'vm-2'],
        hostId: 'h1',
        message: 'warning',
      }
      const map = violationsByDomain([v1])
      expect(map['vm-1']).toBe(v1)
      expect(map['vm-2']).toBe(v1)
    })

    it('prioritizes hard violations over soft violations for the same domain', () => {
      const soft: PlacementViolation = {
        ruleId: 'r-soft',
        ruleName: 'Soft Rule',
        kind: 'vm_host',
        hard: false,
        domainUuids: ['vm-1'],
        hostId: 'h1',
        message: 'soft warning',
      }
      const hard: PlacementViolation = {
        ruleId: 'r-hard',
        ruleName: 'Hard Rule',
        kind: 'vm_anti_affinity',
        hard: true,
        domainUuids: ['vm-1'],
        hostId: 'h1',
        message: 'hard error',
      }

      // soft first, hard second
      const map1 = violationsByDomain([soft, hard])
      expect(map1['vm-1'].hard).toBe(true)
      expect(map1['vm-1'].ruleId).toBe('r-hard')

      // hard first, soft second
      const map2 = violationsByDomain([hard, soft])
      expect(map2['vm-1'].hard).toBe(true)
      expect(map2['vm-1'].ruleId).toBe('r-hard')
    })
  })

  describe('rankEvacuationCandidates', () => {
    it('filters out candidates with hard violations', () => {
      const candidates: EvacuationCandidate[] = [
        { id: 'h1', freeKiB: 10000, hard: ['rule 1 violated'], soft: [] },
        { id: 'h2', freeKiB: 5000, hard: [], soft: [] },
      ]
      const ranked = rankEvacuationCandidates(candidates)
      expect(ranked).toHaveLength(1)
      expect(ranked[0].id).toBe('h2')
    })

    it('returns empty array if all candidates have hard violations', () => {
      const candidates: EvacuationCandidate[] = [
        { id: 'h1', freeKiB: 10000, hard: ['rule 1 violated'], soft: [] },
        { id: 'h2', freeKiB: 8000, hard: ['rule 2 violated'], soft: [] },
      ]
      expect(rankEvacuationCandidates(candidates)).toEqual([])
    })

    it('prefers candidates without soft warnings over candidates with soft warnings even with more memory', () => {
      const candidates: EvacuationCandidate[] = [
        { id: 'h-with-soft', freeKiB: 20000, hard: [], soft: ['prefer not here'] },
        { id: 'h-clean', freeKiB: 5000, hard: [], soft: [] },
      ]
      const ranked = rankEvacuationCandidates(candidates)
      expect(ranked.map((c) => c.id)).toEqual(['h-clean', 'h-with-soft'])
    })

    it('sorts candidates with equal soft status by most free memory first', () => {
      const candidates: EvacuationCandidate[] = [
        { id: 'h-low', freeKiB: 4000, hard: [], soft: [] },
        { id: 'h-high', freeKiB: 12000, hard: [], soft: [] },
        { id: 'h-mid', freeKiB: 8000, hard: [], soft: [] },
      ]
      const ranked = rankEvacuationCandidates(candidates)
      expect(ranked.map((c) => c.id)).toEqual(['h-high', 'h-mid', 'h-low'])
    })
  })

  describe('clusterInventoryMachines', () => {
    it('unwraps { domains: [...] } object response correctly', async () => {
      const spy = vi.spyOn(api, 'get').mockResolvedValueOnce({
        domains: [{ uuid: 'u1', name: 'vm-1', state: 'running', vcpus: 2, memoryKiB: 2048, cpuPercent: 10, autostart: true, hostId: 'h1', hostName: 'Host 1' }],
      } as any)
      const res = await ent.clusterInventoryMachines()
      expect(Array.isArray(res)).toBe(true)
      expect(res).toHaveLength(1)
      expect(res[0].name).toBe('vm-1')
      spy.mockRestore()
    })

    it('handles flat array response correctly', async () => {
      const spy = vi.spyOn(api, 'get').mockResolvedValueOnce([
        { uuid: 'u2', name: 'vm-2', state: 'running', vcpus: 4, memoryKiB: 4096, cpuPercent: 20, autostart: true, hostId: 'h2', hostName: 'Host 2' },
      ] as any)
      const res = await ent.clusterInventoryMachines()
      expect(Array.isArray(res)).toBe(true)
      expect(res).toHaveLength(1)
      expect(res[0].name).toBe('vm-2')
      spy.mockRestore()
    })
  })
})
