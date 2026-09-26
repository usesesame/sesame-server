import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import type { Account, AccountBootstrap } from '../src/lib/auth'

const fetchMock = vi.fn()
vi.stubGlobal('fetch', fetchMock)

function jsonResponse(body: unknown, status = 200, headers: Record<string, string> = {}) {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json', ...headers } })
}

function account(overrides: Partial<Account> = {}): Account {
  return { id: 'account-test', email: 'tester@example.invalid', emailVerified: true, betaAccess: true, ...overrides }
}

function bootstrap(overrides: Partial<AccountBootstrap> = {}): AccountBootstrap {
  return {
    account: account(),
    access: { betaAccess: true, emailVerified: true, downloadsAllowed: true, licences: [] },
    licences: [],
    capabilities: { desktopLinking: true, passkeys: true, browserHelper: false, notifications: true },
    notificationCounts: { security: 0, support: 0, product: 0 },
    security: { activeSessions: 1, connectedDesktops: 0, recentAuthenticationAt: '2026-08-31T10:00:00Z' },
    ...overrides,
  }
}

beforeEach(() => {
  vi.resetModules()
})

afterEach(() => {
  fetchMock.mockReset()
  delete (navigator as unknown as Record<string, unknown>).onLine
})

test('loads an authenticated account', async () => {
  fetchMock.mockResolvedValueOnce(jsonResponse({ user: account() }))
  const { loadAuthState } = await import('../src/lib/auth')
  await expect(loadAuthState()).resolves.toEqual({ state: 'authenticated', account: account() })
  expect(fetchMock).toHaveBeenCalledWith('https://api.test.invalid/v1/auth/me', expect.objectContaining({ credentials: 'include' }))
})

test('reports an expired anonymous session', async () => {
  fetchMock.mockResolvedValueOnce(jsonResponse({ error: { code: 'session_expired', message: 'The session expired.' } }, 401))
  const { loadAuthState } = await import('../src/lib/auth')
  await expect(loadAuthState()).resolves.toEqual({ state: 'anonymous', expired: true })
})

test('reports an ordinary anonymous session', async () => {
  fetchMock.mockResolvedValueOnce(jsonResponse({ error: { code: 'unauthenticated', message: 'Sign in.' } }, 401))
  const { loadAuthState } = await import('../src/lib/auth')
  await expect(loadAuthState()).resolves.toEqual({ state: 'anonymous', expired: false })
})

test('reports a service error with the response code', async () => {
  fetchMock.mockResolvedValueOnce(jsonResponse({ error: { code: 'service_unavailable', message: 'Maintenance in progress.' } }, 503))
  const { loadAuthState } = await import('../src/lib/auth')
  const state = await loadAuthState()
  expect(state.state).toBe('error')
  if (state.state !== 'error') throw new Error('expected an error state')
  expect(state.error.message).toBe('Maintenance in progress.')
  expect(state.error.code).toBe('service_unavailable')
  expect(state.error.status).toBe(503)
})

test('keeps the previous account when the network is offline', async () => {
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: false })
  fetchMock.mockRejectedValueOnce(new TypeError('fetch failed'))
  const previous = account()
  const { loadAuthState } = await import('../src/lib/auth')
  await expect(loadAuthState(previous)).resolves.toEqual({ state: 'offline', account: previous })
})

test('currentAccount returns null for an anonymous session', async () => {
  fetchMock.mockResolvedValueOnce(jsonResponse({ error: { code: 'unauthenticated' } }, 401))
  const { currentAccount } = await import('../src/lib/auth')
  await expect(currentAccount()).resolves.toBeNull()
})

test('currentAccount rejects instead of returning null during an outage', async () => {
  fetchMock.mockResolvedValueOnce(jsonResponse({ error: { code: 'service_unavailable', message: 'Down for maintenance.' } }, 503))
  const { currentAccount } = await import('../src/lib/auth')
  await expect(currentAccount()).rejects.toMatchObject({ code: 'service_unavailable', status: 503 })
})

test('signs in with the CSRF token and returns the account', async () => {
  fetchMock.mockImplementation(async (url: string) => url.endsWith('/v1/auth/csrf')
    ? jsonResponse({ token: 'csrf-test' })
    : jsonResponse({ user: account() }))
  const { signIn } = await import('../src/lib/auth')
  await expect(signIn('tester@example.invalid', 'correct horse battery staple')).resolves.toEqual(account())
  const [url, init] = fetchMock.mock.calls[1]
  expect(url).toBe('https://api.test.invalid/v1/auth/login')
  expect(init.method).toBe('POST')
  expect(JSON.parse(init.body)).toEqual({ email: 'tester@example.invalid', password: 'correct horse battery staple' })
  expect(new Headers(init.headers).get('X-Sesame-CSRF')).toBe('csrf-test')
})

test('signIn surfaces the API error for rejected credentials', async () => {
  fetchMock.mockImplementation(async (url: string) => url.endsWith('/v1/auth/csrf')
    ? jsonResponse({ token: 'csrf-test' })
    : jsonResponse({ error: { code: 'invalid_credentials', message: 'Email or password is incorrect.' } }, 401))
  const { signIn } = await import('../src/lib/auth')
  await expect(signIn('tester@example.invalid', 'wrong password')).rejects.toMatchObject({
    code: 'invalid_credentials',
    status: 401,
    message: 'Email or password is incorrect.',
  })
})

test('signOut tolerates an already ended session', async () => {
  fetchMock.mockImplementation(async (url: string) => url.endsWith('/v1/auth/csrf')
    ? jsonResponse({ token: 'csrf-test' })
    : jsonResponse({ error: { code: 'unauthenticated' } }, 401))
  const { signOut } = await import('../src/lib/auth')
  await expect(signOut()).resolves.toBeUndefined()
})

test('signOut rejects when the server fails', async () => {
  fetchMock.mockImplementation(async (url: string) => url.endsWith('/v1/auth/csrf')
    ? jsonResponse({ token: 'csrf-test' })
    : jsonResponse({ error: { code: 'service_unavailable', message: 'Down for maintenance.' } }, 503))
  const { signOut } = await import('../src/lib/auth')
  await expect(signOut()).rejects.toMatchObject({ code: 'service_unavailable', status: 503 })
})

test('reuses the cached bootstrap while its ETag is current', async () => {
  fetchMock.mockResolvedValueOnce(jsonResponse(bootstrap(), 200, { ETag: '"revision-1"' }))
  fetchMock.mockResolvedValueOnce(new Response(null, { status: 304 }))
  const { getAccountBootstrap } = await import('../src/lib/auth')
  await expect(getAccountBootstrap()).resolves.toEqual(bootstrap())
  await expect(getAccountBootstrap()).resolves.toEqual(bootstrap())
  expect(fetchMock).toHaveBeenCalledTimes(2)
  const init = fetchMock.mock.calls[1][1]
  expect(new Headers(init.headers).get('If-None-Match')).toBe('"revision-1"')
})

test('saves notification preferences with a PATCH', async () => {
  fetchMock.mockImplementation(async (url: string) => url.endsWith('/v1/auth/csrf')
    ? jsonResponse({ token: 'csrf-test' })
    : new Response(null, { status: 204 }))
  const { updateNotificationPreferences } = await import('../src/lib/auth')
  const preferences = { betaReleases: true, supportReplies: false, productAnnouncements: true }
  await expect(updateNotificationPreferences(preferences)).resolves.toBeUndefined()
  const [url, init] = fetchMock.mock.calls[1]
  expect(url).toBe('https://api.test.invalid/v1/account/notifications')
  expect(init.method).toBe('PATCH')
  expect(JSON.parse(init.body)).toEqual(preferences)
})
