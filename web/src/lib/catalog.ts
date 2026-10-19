// Catalog helpers (ADR-0065): pure functions for the Catalog page, its dialogs and the
// unit tests. The store-free twin of projects.ts.

import type { CatalogRequest, PricingConfig, SizePreset } from './api'

/** Hours the showback prices vCPU and RAM for in a month (the mean over the years). */
export const MONTH_HOURS = 24 * 30.5

/** The monthly showback of one machine shape at the given rates, rounded to whole cents. */
export function monthlyCost(vcpus: number, memoryMiB: number, diskGiB: number, rates: PricingConfig): number {
  return round2(
    rates.vcpuHour * vcpus * MONTH_HOURS +
      rates.ramGiBHour * (memoryMiB / 1024) * MONTH_HOURS +
      rates.diskGiBMonth * diskGiB,
  )
}

/** The monthly cost of a size preset — the number the order dialog quotes. */
export function presetCost(preset: Pick<SizePreset, 'vcpus' | 'memoryMiB' | 'diskGiB'>, rates: PricingConfig): number {
  return monthlyCost(preset.vcpus, preset.memoryMiB, preset.diskGiB, rates)
}

const round2 = (v: number) => Math.round(v * 100) / 100

export type BadgeKind = 'pending' | 'info' | 'ok' | 'muted' | 'danger'

/** Color group + English label of a request status; the UI runs the label through t(). */
export function statusBadge(status: CatalogRequest['status']): { label: string; kind: BadgeKind } {
  switch (status) {
    case 'pending_approval':
      return { label: 'Pending', kind: 'pending' }
    case 'approved':
      return { label: 'Approved', kind: 'info' }
    case 'provisioning':
      return { label: 'Provisioning', kind: 'info' }
    case 'active':
      return { label: 'Active', kind: 'ok' }
    case 'expired':
      return { label: 'Expired', kind: 'muted' }
    case 'rejected':
    case 'failed':
      return { label: status === 'rejected' ? 'Rejected' : 'Failed', kind: 'danger' }
  }
}

/** Whole days left on a lease (negative once it is over). */
export function leaseDaysLeft(expiry: string, now = Date.now()): number {
  return Math.ceil((Date.parse(expiry) - now) / 86400e3)
}

