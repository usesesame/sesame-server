import { vi } from 'vitest'

export type FakeCall = { method: string; path: string; body: unknown; headers: Headers }
export type FakeRoute = (call: FakeCall) => Response | object | undefined | Promise<Response | object | undefined>

export const owner = { id: 'own-1', name: 'Alex Rivera' }
export const session = { owner, csrfToken: 'csrf-one', recentAuthUntil: '2099-01-01T00:00:00Z', expiresAt: '2099-01-02T00:00:00Z' }
export const member = { id: 'mem-1', name: 'Priya Raman', createdAt: '2026-08-31T10:00:00Z', deviceCount: 2 }
export const device = {
  id: 'dev-1',
  name: 'Studio desktop',
  holder: { kind: 'owner', id: 'own-1', name: 'Alex Rivera' },
  appVersion: '0.3.1',
  platform: 'Windows 11',
  architecture: 'x86_64',
  createdAt: '2026-08-01T10:00:00Z',
  expiresAt: '2026-11-01T10:00:00Z',
  lastSeenAt: '2026-10-09T10:00:00Z',
}

export const upToDate = {
  configured: true,
  enabled: true,
  channel: 'stable',
  installKind: 'container',
  current: { product: 'sesame-server', version: '0.1.0' },
  latest: { version: '0.1.0', publishedAt: '2026-10-01T00:00:00Z', notesUrl: 'https://updates.example.test/notes/0.1.0', security: false, minimumFrom: '', images: [], binaries: [] },
  available: false,
  checkedAt: '2026-10-10T08:00:00Z',
  error: '',
  commands: [],
}

export function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

export function apiError(status: number, code: string, message: string): Response {
  return json({ error: { code, message } }, status)
}

export function installFakeApi(routes: Record<string, FakeRoute>) {
  const calls: FakeCall[] = []
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), 'http://console.test')
    const method = (init?.method ?? 'GET').toUpperCase()
    const path = url.pathname + url.search
    const text = typeof init?.body === 'string' ? init.body : ''
    const call: FakeCall = { method, path, body: text ? JSON.parse(text) : undefined, headers: new Headers(init?.headers) }
    calls.push(call)
    const route = routes[`${method} ${path}`] ?? routes[`${method} ${url.pathname}`]
    if (!route) return apiError(404, 'not_found', `No fake route for ${method} ${path}`)
    const result = await route(call)
    if (result === undefined) return new Response(null, { status: 204 })
    return result instanceof Response ? result : json(result)
  })
  vi.stubGlobal('fetch', fetchMock)
  return { calls, fetchMock }
}
