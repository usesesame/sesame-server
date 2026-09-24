import { afterEach, expect, test, vi } from 'vitest'

const fetchMock = vi.fn()
vi.stubGlobal('fetch', fetchMock)

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

afterEach(() => {
  fetchMock.mockReset()
})

test('retries a mutation once with a fresh token after invalid_csrf', async () => {
  vi.resetModules()
  const responses = [
    jsonResponse({ token: 'stale-token' }),
    jsonResponse({ error: { code: 'invalid_csrf', message: 'The admin security check expired.' } }, 403),
    jsonResponse({ token: 'fresh-token' }),
    jsonResponse({ updated: true }),
  ]
  fetchMock.mockImplementation(async () => responses.shift())
  const { request } = await import('../src/lib/api')
  const result = await request('/v1/admin/flags/desktop_linking_enabled', { method: 'PATCH', body: JSON.stringify({ enabled: true }) })
  expect(result).toEqual({ updated: true })
  expect(fetchMock).toHaveBeenCalledTimes(4)
  const retry = fetchMock.mock.calls[3]?.[1] as RequestInit
  expect(new Headers(retry.headers).get('X-Sesame-CSRF')).toBe('fresh-token')
})

test('a second invalid_csrf surfaces the error instead of retrying again', async () => {
  vi.resetModules()
  const responses = [
    jsonResponse({ token: 'stale-token' }),
    jsonResponse({ error: { code: 'invalid_csrf', message: 'The admin security check expired.' } }, 403),
    jsonResponse({ token: 'still-stale' }),
    jsonResponse({ error: { code: 'invalid_csrf', message: 'The admin security check expired.' } }, 403),
  ]
  fetchMock.mockImplementation(async () => responses.shift())
  const { request } = await import('../src/lib/api')
  await expect(request('/v1/admin/flags/desktop_linking_enabled', { method: 'PATCH', body: '{}' })).rejects.toMatchObject({
    code: 'invalid_csrf',
    status: 403,
  })
  expect(fetchMock).toHaveBeenCalledTimes(4)
})
