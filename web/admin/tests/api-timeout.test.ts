import { afterEach, expect, test, vi } from 'vitest'

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
  vi.resetModules()
})

test('reports a timeout when the admin service does not answer', async () => {
  vi.useFakeTimers()
  vi.stubGlobal('fetch', vi.fn((_url: string, init?: RequestInit) => new Promise<Response>((_resolve, reject) => {
    init?.signal?.addEventListener('abort', () => reject(new DOMException('The operation was aborted.', 'AbortError')))
  })))
  const { request } = await import('../src/lib/api')
  const pending = request('/v1/admin/overview')
  const assertion = expect(pending).rejects.toMatchObject({
    code: 'timeout',
    status: 0,
    message: 'The Sesame admin service did not respond. Please try again.',
  })
  await vi.advanceTimersByTimeAsync(8_000)
  await assertion
})

test('reports offline when the browser has no connection', async () => {
  vi.stubGlobal('fetch', vi.fn(() => Promise.reject(new TypeError('Failed to fetch'))))
  vi.stubGlobal('navigator', { onLine: false })
  const { request } = await import('../src/lib/api')
  await expect(request('/v1/admin/overview')).rejects.toMatchObject({
    code: 'offline',
    status: 0,
    message: 'You appear to be offline. Please reconnect and try again.',
  })
})
