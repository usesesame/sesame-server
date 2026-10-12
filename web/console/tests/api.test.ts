import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { APIError, clearCSRFToken, mutate, onSessionEnded, onStepUpRequired, request, setAPIBase } from '../src/lib/api'
import { apiError, installFakeApi, json, session } from './support/fake-api'

beforeEach(() => {
  clearCSRFToken()
  setAPIBase('')
})

afterEach(() => {
  onStepUpRequired(null)
  onSessionEnded(null)
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

test('sends no CSRF header on reads and fetches the token from the session before the first write', async () => {
  const { calls } = installFakeApi({
    'GET /v1/owner/session': () => session,
    'GET /v1/owner/devices': () => ({ devices: [] }),
    'POST /v1/owner/members': () => ({ member: {} }),
  })
  await request('/v1/owner/devices')
  expect(calls).toHaveLength(1)
  expect(calls[0].headers.get('X-Sesame-CSRF')).toBeNull()
  await mutate('/v1/owner/members', 'POST', { name: 'Priya Raman' })
  const write = calls.at(-1)!
  expect(write.path).toBe('/v1/owner/members')
  expect(write.headers.get('X-Sesame-CSRF')).toBe('csrf-one')
  expect(write.headers.get('Content-Type')).toBe('application/json')
  expect(calls.filter((call) => call.path === '/v1/owner/session')).toHaveLength(1)
})

test('does not fetch or send a CSRF token for anonymous writes', async () => {
  const { calls } = installFakeApi({ 'POST /v1/owner/login': () => session })
  await mutate('/v1/owner/login', 'POST', { name: 'a', password: 'b', code: '123456' }, { anonymous: true })
  expect(calls).toHaveLength(1)
  expect(calls[0].headers.get('X-Sesame-CSRF')).toBeNull()
})

test('retries a write once with a fresh token after invalid_csrf', async () => {
  let tokens = 0
  let writes = 0
  const { calls } = installFakeApi({
    'GET /v1/owner/session': () => ({ ...session, csrfToken: `csrf-${++tokens}` }),
    'PATCH /v1/owner/settings': () => (++writes === 1 ? apiError(403, 'invalid_csrf', 'Reload the console and try again.') : { name: 'Home' }),
  })
  await expect(mutate('/v1/owner/settings', 'PATCH', { name: 'Home' })).resolves.toEqual({ name: 'Home' })
  const sent = calls.filter((call) => call.method === 'PATCH').map((call) => call.headers.get('X-Sesame-CSRF'))
  expect(sent).toEqual(['csrf-1', 'csrf-2'])
})

test('a second invalid_csrf surfaces the error', async () => {
  let tokens = 0
  const { calls } = installFakeApi({
    'GET /v1/owner/session': () => ({ ...session, csrfToken: `csrf-${++tokens}` }),
    'PATCH /v1/owner/settings': () => apiError(403, 'invalid_csrf', 'Reload the console and try again.'),
  })
  await expect(mutate('/v1/owner/settings', 'PATCH', {})).rejects.toMatchObject({ status: 403, code: 'invalid_csrf' })
  expect(calls.filter((call) => call.method === 'PATCH')).toHaveLength(2)
})

test('asks for step-up and repeats the request once after it succeeds', async () => {
  let attempts = 0
  const { calls } = installFakeApi({
    'GET /v1/owner/session': () => session,
    'DELETE /v1/owner/devices/dev-1': () => (++attempts === 1 ? apiError(403, 'step_up_required', 'Confirm your password and a current code to continue.') : undefined),
  })
  const handler = vi.fn(async () => true)
  onStepUpRequired(handler)
  await expect(mutate('/v1/owner/devices/dev-1', 'DELETE')).resolves.toBeUndefined()
  expect(handler).toHaveBeenCalledTimes(1)
  expect(calls.filter((call) => call.method === 'DELETE')).toHaveLength(2)
})

test('reports a cancelled step-up and does not repeat the request', async () => {
  const { calls } = installFakeApi({
    'GET /v1/owner/session': () => session,
    'DELETE /v1/owner/devices/dev-1': () => apiError(403, 'step_up_required', 'Confirm your password and a current code to continue.'),
  })
  onStepUpRequired(async () => false)
  await expect(mutate('/v1/owner/devices/dev-1', 'DELETE')).rejects.toMatchObject({ code: 'step_up_cancelled', message: 'The action was cancelled.' })
  expect(calls.filter((call) => call.method === 'DELETE')).toHaveLength(1)
})

test('asks for step-up only once when the server keeps refusing', async () => {
  const { calls } = installFakeApi({
    'GET /v1/owner/session': () => session,
    'DELETE /v1/owner/devices/dev-1': () => apiError(403, 'step_up_required', 'Confirm your password and a current code to continue.'),
  })
  const handler = vi.fn(async () => true)
  onStepUpRequired(handler)
  await expect(mutate('/v1/owner/devices/dev-1', 'DELETE')).rejects.toMatchObject({ code: 'step_up_required' })
  expect(handler).toHaveBeenCalledTimes(1)
  expect(calls.filter((call) => call.method === 'DELETE')).toHaveLength(2)
})

test('refreshes the token after invalid_csrf and still allows a step-up on the retry', async () => {
  const outcomes = [apiError(403, 'invalid_csrf', 'Reload the console and try again.'), apiError(403, 'step_up_required', 'Confirm.'), json({ ok: true })]
  installFakeApi({
    'GET /v1/owner/session': () => session,
    'POST /v1/owner/export': () => outcomes.shift(),
  })
  const handler = vi.fn(async () => true)
  onStepUpRequired(handler)
  await expect(mutate('/v1/owner/export', 'POST')).resolves.toEqual({ ok: true })
  expect(handler).toHaveBeenCalledTimes(1)
})

test('treats a missing step-up handler as a plain error', async () => {
  installFakeApi({
    'GET /v1/owner/session': () => session,
    'POST /v1/owner/export': () => apiError(403, 'step_up_required', 'Confirm your password and a current code to continue.'),
  })
  await expect(mutate('/v1/owner/export', 'POST')).rejects.toMatchObject({ status: 403, code: 'step_up_required' })
})

test.each(['not_authenticated', 'session_expired'])('tells the app the session ended on a 401 with %s', async (code) => {
  installFakeApi({ 'GET /v1/owner/devices': () => apiError(401, code, 'Sign in to continue.') })
  const ended = vi.fn()
  onSessionEnded(ended)
  await expect(request('/v1/owner/devices')).rejects.toMatchObject({ status: 401, code })
  expect(ended).toHaveBeenCalledTimes(1)
})

test('does not end the session when a sign-in attempt fails', async () => {
  installFakeApi({ 'POST /v1/owner/step-up': () => apiError(401, 'invalid_credentials', 'The password or code is incorrect.') })
  const ended = vi.fn()
  onSessionEnded(ended)
  await expect(mutate('/v1/owner/step-up', 'POST', {}, { anonymous: true })).rejects.toMatchObject({ status: 401, code: 'invalid_credentials' })
  expect(ended).not.toHaveBeenCalled()
})

test('uses the server message and keeps the status and code on errors', async () => {
  installFakeApi({ 'GET /v1/owner/audit': () => apiError(503, 'unavailable', 'The server could not complete that action and no change was committed.') })
  const error = await request('/v1/owner/audit').catch((reason: unknown) => reason)
  expect(error).toBeInstanceOf(APIError)
  expect(error).toMatchObject({ status: 503, code: 'unavailable', message: 'The server could not complete that action and no change was committed.' })
})

test('falls back to a plain message when the error body is not JSON', async () => {
  vi.stubGlobal('fetch', vi.fn(async () => new Response('<html>bad gateway</html>', { status: 502 })))
  await expect(request('/v1/owner/devices')).rejects.toMatchObject({ status: 502, message: 'The request could not be completed.' })
})

test('reports an unreachable server', async () => {
  vi.stubGlobal('fetch', vi.fn(async () => { throw new TypeError('Failed to fetch') }))
  await expect(request('/v1/owner/devices')).rejects.toMatchObject({ status: 0, code: 'network_error' })
})

test('reports a timeout when the server does not answer', async () => {
  vi.useFakeTimers()
  vi.stubGlobal('fetch', vi.fn((_input: unknown, init?: RequestInit) => new Promise((_resolve, reject) => {
    init?.signal?.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')))
  })))
  const pending = request('/v1/owner/devices').catch((reason: unknown) => reason)
  await vi.advanceTimersByTimeAsync(8_001)
  expect(await pending).toMatchObject({ status: 0, code: 'timeout' })
})

test('sends same-origin credentials and returns undefined for 204', async () => {
  const { fetchMock } = installFakeApi({ 'DELETE /v1/owner/pairings/p1': () => undefined, 'GET /v1/owner/session': () => session })
  await expect(mutate('/v1/owner/pairings/p1', 'DELETE')).resolves.toBeUndefined()
  expect((fetchMock.mock.calls.at(-1)![1] as RequestInit).credentials).toBe('same-origin')
})

test('refuses an API base that points at another origin', () => {
  expect(() => setAPIBase('https://elsewhere.example')).toThrow('own origin')
  expect(() => setAPIBase('//elsewhere.example')).toThrow('own origin')
  expect(() => setAPIBase('')).not.toThrow()
  expect(() => setAPIBase('/console-api')).not.toThrow()
})
