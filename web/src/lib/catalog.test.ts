import { describe, expect, it } from 'vitest'
import { MONTH_HOURS, leaseDaysLeft, monthlyCost, presetCost, statusBadge } from './catalog'
import type { CatalogRequestStatus, PricingConfig, SizePreset } from './api'

const rates: PricingConfig = { vcpuHour: 0.04, ramGiBHour: 0.01, diskGiBMonth: 0.08, currency: '$' }

const preset = (over: Partial<SizePreset>): SizePreset => ({ id: 'p', name: 'Preset', vcpus: 2, memoryMiB: 2048, diskGiB: 20, ...over })

const round2 = (v: number) => Math.round(v * 100) / 100

describe('MONTH_HOURS', () => {
  it('equals 732 hours per month', () => {
    expect(MONTH_HOURS).toBe(24 * 30.5)
    expect(MONTH_HOURS).toBe(732)
  })
})

describe('monthlyCost', () => {
  it('calculates cost from vCPUs, memory, and disk correctly', () => {
    // 0.04 * 2 * 732 + 0.01 * (2048 / 1024) * 732 + 0.08 * 20 = 58.56 + 14.64 + 1.6 = 74.8
    expect(monthlyCost(2, 2048, 20, rates)).toBeCloseTo(74.8, 5)
  })

  it('matches presetCost for the same shape', () => {
    expect(monthlyCost(4, 8192, 60, rates)).toBe(presetCost(preset({ vcpus: 4, memoryMiB: 8192, diskGiB: 60 }), rates))
  })

  it('handles fractional GiB memory accurately', () => {
    // 1536 MiB = 1.5 GiB
    // 0.04 * 1 * 732 + 0.01 * 1.5 * 732 + 0.08 * 10 = 29.28 + 10.98 + 0.8 = 41.06
    expect(monthlyCost(1, 1536, 10, rates)).toBeCloseTo(41.06, 2)
  })

  it('handles zero resources and zero rates', () => {
    expect(monthlyCost(0, 0, 0, rates)).toBe(0)
    expect(monthlyCost(4, 4096, 100, { ...rates, vcpuHour: 0, ramGiBHour: 0, diskGiBMonth: 0 })).toBe(0)
  })
})

describe('presetCost', () => {
  it('prices a preset over the showback month', () => {
    expect(presetCost(preset({ vcpus: 2, memoryMiB: 2048, diskGiB: 20 }), rates)).toBeCloseTo(74.8, 5)
  })

  it('scales with preset sizes', () => {
    expect(presetCost(preset({ vcpus: 4, memoryMiB: 8192, diskGiB: 60 }), rates)).toBeCloseTo(180.48, 5)
  })

  it('rounds to whole cents according to rates', () => {
    const cheap = { vcpuHour: 0.001, ramGiBHour: 0.001, diskGiBMonth: 0.001, currency: '€' }
    expect(presetCost(preset({ vcpus: 3, memoryMiB: 1024, diskGiB: 10 }), cheap)).toBe(round2(0.001 * 3 * 732 + 0.001 * 1 * 732 + 0.001 * 10))
  })
})

describe('statusBadge', () => {
  const cases: [CatalogRequestStatus, string, string][] = [
    ['pending_approval', 'Pending', 'pending'],
    ['approved', 'Approved', 'info'],
    ['provisioning', 'Provisioning', 'info'],
    ['active', 'Active', 'ok'],
    ['expired', 'Expired', 'muted'],
    ['rejected', 'Rejected', 'danger'],
    ['failed', 'Failed', 'danger'],
  ]

  it('resolves every request status to the expected label and badge kind', () => {
    for (const [status, label, kind] of cases) {
      expect(statusBadge(status)).toEqual({ label, kind })
    }
  })
})

describe('leaseDaysLeft', () => {
  const now = Date.parse('2026-06-01T12:00:00Z')

  it('counts whole days remaining to expiry', () => {
    expect(leaseDaysLeft('2026-06-08T12:00:00Z', now)).toBe(7)
    expect(leaseDaysLeft('2026-06-02T12:00:00Z', now)).toBe(1)
    expect(leaseDaysLeft('2026-06-01T18:00:00Z', now)).toBe(1)
    expect(leaseDaysLeft('2026-06-01T12:00:00Z', now)).toBe(0)
  })

  it('returns negative numbers once the lease has expired', () => {
    expect(leaseDaysLeft('2026-06-01T11:59:59Z', now)).toBe(-0)
    expect(leaseDaysLeft('2026-05-31T12:00:00Z', now)).toBe(-1)
    expect(leaseDaysLeft('2026-05-25T12:00:00Z', now)).toBe(-7)
  })
})
