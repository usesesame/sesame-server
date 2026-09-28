import { apiURL } from './runtime-config'

export { apiURL }
let csrf = ''

const REQUEST_TIMEOUT_MS = 8_000

export class APIError extends Error {
  constructor(message: string, public status: number, public code = '') { super(message) }
}

async function timedFetch(path: string, init: RequestInit = {}): Promise<Response> {
  const controller = new AbortController()
  const timeout = window.setTimeout(() => controller.abort(), REQUEST_TIMEOUT_MS)

  try {
    return await fetch(`${apiURL}${path}`, {
      ...init,
      credentials: 'include',
      signal: controller.signal,
    })
  } catch (reason) {
    if (reason instanceof DOMException && reason.name === 'AbortError') {
      throw new APIError('The Sesame admin service did not respond. Please try again.', 0, 'timeout')
    }
    const offline = typeof navigator !== 'undefined' && !navigator.onLine
    throw new APIError(
      offline ? 'You appear to be offline. Please reconnect and try again.' : 'The Sesame admin service could not be reached. Please try again.',
      0,
      offline ? 'offline' : 'network_error',
    )
  } finally {
    window.clearTimeout(timeout)
  }
}

async function csrfToken() {
  if (csrf) return csrf
  const response = await timedFetch('/v1/admin/auth/csrf')
  if (!response.ok) throw new APIError('The admin service is unavailable.', response.status)
  csrf = (await response.json() as { token: string }).token
  return csrf
}

export async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  return perform<T>(path, init, true)
}

// The CSRF cookie expires after an hour while the admin session lasts eight, so
// a stale token is normal use, not an error. Clear the cached value and retry
// the same request once with a fresh token.
async function perform<T>(path: string, init: RequestInit, allowRetry: boolean): Promise<T> {
  const method = (init.method || 'GET').toUpperCase()
  const headers = new Headers(init.headers)
  if (!['GET', 'HEAD', 'OPTIONS'].includes(method)) headers.set('X-Sesame-CSRF', await csrfToken())
  if (init.body) headers.set('Content-Type', 'application/json')
  const response = await timedFetch(path, { ...init, headers })
  if (response.status === 204) return undefined as T
  const body = await response.json().catch(() => ({})) as { error?: { code?: string; message?: string } }
  if (!response.ok) {
    if (response.status === 403 && body.error?.code === 'invalid_csrf') {
      csrf = ''
      if (allowRetry) return perform<T>(path, init, false)
    }
    throw new APIError(body.error?.message || 'The request could not be completed.', response.status, body.error?.code)
  }
  return body as T
}

export function mutate<T>(path: string, method: 'POST' | 'PATCH' | 'PUT' | 'DELETE', body?: unknown) {
  return request<T>(path, { method, body: body === undefined ? undefined : JSON.stringify(body) })
}
