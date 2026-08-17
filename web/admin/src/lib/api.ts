import { apiURL } from './runtime-config'

export { apiURL }
let csrf = ''

export class APIError extends Error {
  constructor(message: string, public status: number, public code = '') { super(message) }
}

async function csrfToken() {
  if (csrf) return csrf
  const response = await fetch(`${apiURL}/v1/admin/auth/csrf`, { credentials: 'include' })
  if (!response.ok) throw new APIError('The admin service is unavailable.', response.status)
  csrf = (await response.json() as { token: string }).token
  return csrf
}

export async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const method = (init.method || 'GET').toUpperCase()
  const headers = new Headers(init.headers)
  if (!['GET', 'HEAD', 'OPTIONS'].includes(method)) headers.set('X-Sesame-CSRF', await csrfToken())
  if (init.body) headers.set('Content-Type', 'application/json')
  const response = await fetch(`${apiURL}${path}`, { ...init, headers, credentials: 'include' })
  if (response.status === 204) return undefined as T
  const body = await response.json().catch(() => ({})) as { error?: { code?: string; message?: string } }
  if (!response.ok) {
    if (response.status === 403 && body.error?.code === 'csrf_invalid') csrf = ''
    throw new APIError(body.error?.message || 'The request could not be completed.', response.status, body.error?.code)
  }
  return body as T
}

export function mutate<T>(path: string, method: 'POST' | 'PATCH' | 'PUT' | 'DELETE', body?: unknown) {
  return request<T>(path, { method, body: body === undefined ? undefined : JSON.stringify(body) })
}
