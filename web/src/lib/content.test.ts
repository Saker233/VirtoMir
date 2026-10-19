import { describe, expect, it, vi, beforeEach, beforeAll } from 'vitest'
import type { ContentDeploySpec, ContentHostConfig, ContentItem, ContentVersion, PublishContentReq, PublishContentVersionReq } from './api'

let api: (typeof import('./api'))['api']

function mockFetch(status: number, data?: any) {
  const jsonStr = data !== undefined ? JSON.stringify(data) : ''
  vi.stubGlobal(
    'fetch',
    vi.fn().mockResolvedValue({
      ok: status >= 200 && status < 300,
      status,
      headers: new Headers({ 'content-type': 'application/json' }),
      text: async () => jsonStr,
      json: async () => data,
    }),
  )
}

describe('Content Library API client', () => {
  beforeAll(async () => {
    if (typeof (globalThis as any).window === 'undefined') {
      ;(globalThis as any).window = { addEventListener: () => {} }
    }
    if (typeof (globalThis as any).location === 'undefined') {
      ;(globalThis as any).location = new URL('http://localhost:8420/')
    }
    const mod = await import('./api')
    api = mod.api
  })

  beforeEach(() => {
    vi.restoreAllMocks()
  })

  it('contentItems requests GET /api/v1/content/items', async () => {
    const mockItems: ContentItem[] = [
      {
        id: 'ci-1',
        name: 'Ubuntu 24.04',
        type: 'image',
        sourceHost: 'hv-01',
        keepVersions: 3,
        syncPolicy: { mode: 'all' },
        created: '2026-01-01T00:00:00Z',
        updated: '2026-01-01T00:00:00Z',
      },
    ]

    mockFetch(200, { items: mockItems })

    const res = await api.contentItems()
    expect(res).toEqual(mockItems)
    expect(fetch).toHaveBeenCalledWith(
      '/api/v1/content/items',
      expect.objectContaining({ method: 'GET' }),
    )
  })

  it('contentItem requests GET /api/v1/content/items/:id', async () => {
    const mockItem: ContentItem = {
      id: 'ci-1',
      name: 'Debian 13',
      type: 'iso',
      sourceHost: 'hv-01',
      keepVersions: 3,
      syncPolicy: { mode: 'selected', hosts: ['hv-01', 'hv-02'] },
      created: '2026-01-01T00:00:00Z',
      updated: '2026-01-01T00:00:00Z',
    }

    mockFetch(200, mockItem)

    const res = await api.contentItem('ci-1')
    expect(res).toEqual(mockItem)
    expect(fetch).toHaveBeenCalledWith(
      '/api/v1/content/items/ci-1',
      expect.objectContaining({ method: 'GET' }),
    )
  })

  it('publishContent requests POST /api/v1/content/items', async () => {
    const req: PublishContentReq = {
      name: 'New Template',
      type: 'template',
      templateUuid: 'uuid-123',
      version: '1.0',
      keepVersions: 3,
      syncPolicy: { mode: 'all' },
    }

    mockFetch(201, { item: { id: 'ci-new', ...req } })

    const res = await api.publishContent(req)
    expect(res.item.id).toBe('ci-new')
    expect(fetch).toHaveBeenCalledWith(
      '/api/v1/content/items',
      expect.objectContaining({
        method: 'POST',
        body: JSON.stringify(req),
      }),
    )
  })

  it('updateContent requests PUT /api/v1/content/items/:id', async () => {
    const update = { description: 'Updated desc' }
    mockFetch(200, { item: { id: 'ci-1', ...update } })

    const res = await api.updateContent('ci-1', update)
    expect(res.item.description).toBe('Updated desc')
    expect(fetch).toHaveBeenCalledWith(
      '/api/v1/content/items/ci-1',
      expect.objectContaining({
        method: 'PUT',
        body: JSON.stringify(update),
      }),
    )
  })

  it('deleteContent requests DELETE /api/v1/content/items/:id', async () => {
    mockFetch(204)

    await api.deleteContent('ci-1')
    expect(fetch).toHaveBeenCalledWith(
      '/api/v1/content/items/ci-1',
      expect.objectContaining({ method: 'DELETE' }),
    )
  })

  it('publishContentVersion requests POST /api/v1/content/items/:id/versions', async () => {
    const req: PublishContentVersionReq = {
      version: '2.0',
      description: 'Second version',
    }

    mockFetch(201, {
      item: { id: 'ci-1', name: 'Item' },
      version: { id: 'ver-2', version: '2.0' },
    })

    const res = await api.publishContentVersion('ci-1', req)
    expect(res.version.version).toBe('2.0')
    expect(fetch).toHaveBeenCalledWith(
      '/api/v1/content/items/ci-1/versions',
      expect.objectContaining({
        method: 'POST',
        body: JSON.stringify(req),
      }),
    )
  })

  it('deleteContentVersion requests DELETE /api/v1/content/items/:id/versions/:vid', async () => {
    mockFetch(204)

    await api.deleteContentVersion('ci-1', 'ver-2')
    expect(fetch).toHaveBeenCalledWith(
      '/api/v1/content/items/ci-1/versions/ver-2',
      expect.objectContaining({ method: 'DELETE' }),
    )
  })

  it('syncContent requests POST /api/v1/content/items/:id/sync', async () => {
    mockFetch(202, {
      id: 'job-sync-1',
      kind: 'content-sync',
      title: 'Sync item',
      state: 'running',
    })

    const job = await api.syncContent('ci-1', 'ver-2')
    expect(job.id).toBe('job-sync-1')
    expect(job.kind).toBe('content-sync')
    expect(fetch).toHaveBeenCalledWith(
      '/api/v1/content/items/ci-1/sync',
      expect.objectContaining({
        method: 'POST',
        body: JSON.stringify({ versionId: 'ver-2' }),
      }),
    )
  })

  it('contentConfig and saveContentConfig get and put /api/v1/content/config', async () => {
    const cfg: ContentHostConfig = {
      targetPool: 'fast-pool',
      autoSync: true,
    }

    mockFetch(200, cfg)

    const getRes = await api.contentConfig()
    expect(getRes.targetPool).toBe('fast-pool')
    expect(fetch).toHaveBeenCalledWith(
      '/api/v1/content/config',
      expect.objectContaining({ method: 'GET' }),
    )

    mockFetch(200, cfg)
    const saveRes = await api.saveContentConfig(cfg)
    expect(saveRes.autoSync).toBe(true)
    expect(fetch).toHaveBeenCalledWith(
      '/api/v1/content/config',
      expect.objectContaining({
        method: 'PUT',
        body: JSON.stringify(cfg),
      }),
    )
  })

  it('deployFromContent requests POST /api/v1/content/items/:id/deploy', async () => {
    const spec: ContentDeploySpec = {
      name: 'test-vm',
      pool: 'default',
      vcpus: 2,
      memoryMiB: 2048,
      start: true,
    }

    mockFetch(202, {
      id: 'job-deploy-1',
      kind: 'content-deploy',
      title: 'Deploy test-vm',
      state: 'running',
    })

    const job = await api.deployFromContent('ci-1', spec)
    expect(job.id).toBe('job-deploy-1')
    expect(job.kind).toBe('content-deploy')
    expect(fetch).toHaveBeenCalledWith(
      '/api/v1/content/items/ci-1/deploy',
      expect.objectContaining({
        method: 'POST',
        body: JSON.stringify(spec),
      }),
    )
  })
})
