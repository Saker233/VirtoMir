import { describe, expect, it } from 'vitest'

;(globalThis as any).window = { addEventListener: () => {}, scrollTo: () => {} }
;(globalThis as any).location = { pathname: '/', hash: '', search: '', origin: 'http://localhost' }
;(globalThis as any).history = {
  pushState: (_: any, __: any, url: string) => {
    ;(globalThis as any).location.pathname = url
  },
  replaceState: (_: any, __: any, url: string) => {
    ;(globalThis as any).location.pathname = url
  },
}

const { router } = await import('./router.svelte')

describe('router navigation and switchHost', () => {
  it('retains catalog, library, compliance and project routes when switching hosts', () => {
    const retainedRoutes = ['overview', 'machines', 'storage', 'networks', 'backups', 'alerts', 'activity', 'reports', 'compliance', 'projects', 'catalog', 'library']
    for (const page of retainedRoutes) {
      ;(router as any).route = { name: page }
      ;(globalThis as any).location.pathname = `/${page}`
      router.switchHost('0123456789abcdef')
      expect((globalThis as any).location.pathname).toBe(`/hosts/0123456789abcdef/${page}`)
    }
  })

  it('resets to root for non-retained routes like new machine wizard', () => {
    ;(router as any).route = { name: 'new' }
    ;(globalThis as any).location.pathname = '/machines/new'
    router.switchHost('0123456789abcdef')
    expect((globalThis as any).location.pathname).toBe('/hosts/0123456789abcdef')
  })
})
