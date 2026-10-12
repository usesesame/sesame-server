const REQUEST_TIMEOUT_MS = 8_000
const SAFE_METHODS = ['GET', 'HEAD', 'OPTIONS']
const STEP_UP_CODE = 'step_up_required'
const SESSION_ENDED_CODES = ['not_authenticated', 'session_expired']

export class APIError extends Error {
  readonly status: number
  readonly code: string

  constructor(message: string, status: number, code = '') {
    super(message)
    this.status = status
    this.code = code
  }
}

type StepUpHandler = () => Promise<boolean>

let apiBase = ''
let csrf = ''
let stepUpHandler: StepUpHandler | null = null
let sessionEndedHandler: (() => void) | null = null

export function setAPIBase(base: string) {
  if (base !== '' && !/^\/([^/\\]+(\/[^/\\]+)*)?$/.test(base)) throw new Error('The console must call its own origin.')
  apiBase = base.replace(/\/$/, '')
}

export function setCSRFToken(token: string) {
  csrf = token
}

export function clearCSRFToken() {
  csrf = ''
}

export function onStepUpRequired(handler: StepUpHandler | null) {
  stepUpHandler = handler
}

export function onSessionEnded(handler: (() => void) | null) {
  sessionEndedHandler = handler
}

async function timedFetch(path: string, init: RequestInit = {}, timeoutMs = REQUEST_TIMEOUT_MS): Promise<Response> {
  const controller = new AbortController()
  const timeout = window.setTimeout(() => controller.abort(), timeoutMs)
  try {
    return await fetch(`${apiBase}${path}`, { ...init, credentials: 'same-origin', signal: controller.signal })
  } catch (reason) {
    if (reason instanceof DOMException && reason.name === 'AbortError') {
      throw new APIError('The server did not respond. Try again.', 0, 'timeout')
    }
    const offline = typeof navigator !== 'undefined' && !navigator.onLine
    throw new APIError(
      offline ? 'You appear to be offline. Reconnect and try again.' : 'The server could not be reached. Try again.',
      0,
      offline ? 'offline' : 'network_error',
    )
  } finally {
    window.clearTimeout(timeout)
  }
}

async function csrfToken(): Promise<string> {
  if (csrf) return csrf
  const response = await timedFetch('/v1/owner/session')
  if (response.ok) {
    const body = await response.json().catch(() => ({})) as { csrfToken?: string }
    csrf = body.csrfToken ?? ''
  }
  return csrf
}

type RequestOptions = { timeoutMs?: number; anonymous?: boolean }

export function request<T>(path: string, init: RequestInit = {}, options: RequestOptions = {}): Promise<T> {
  return perform<T>(path, init, options, { csrf: true, stepUp: true })
}

type Retries = { csrf: boolean; stepUp: boolean }

async function perform<T>(path: string, init: RequestInit, options: RequestOptions, retries: Retries): Promise<T> {
  const method = (init.method || 'GET').toUpperCase()
  const headers = new Headers(init.headers)
  if (!SAFE_METHODS.includes(method) && !options.anonymous) {
    const token = await csrfToken()
    if (token) headers.set('X-Sesame-CSRF', token)
  }
  if (init.body) headers.set('Content-Type', 'application/json')
  const response = await timedFetch(path, { ...init, headers }, options.timeoutMs)
  if (response.status === 204) return undefined as T
  const body = await response.json().catch(() => ({})) as { error?: { code?: string; message?: string } }
  if (response.ok) return body as T
  const code = body.error?.code ?? ''
  if (response.status === 403 && code === 'invalid_csrf') {
    csrf = ''
    if (retries.csrf) return perform<T>(path, init, options, { ...retries, csrf: false })
  }
  if (response.status === 403 && code === STEP_UP_CODE && retries.stepUp && stepUpHandler) {
    if (await stepUpHandler()) return perform<T>(path, init, options, { ...retries, stepUp: false })
    throw new APIError('The action was cancelled.', response.status, 'step_up_cancelled')
  }
  if (response.status === 401 && SESSION_ENDED_CODES.includes(code)) {
    csrf = ''
    sessionEndedHandler?.()
  }
  throw new APIError(body.error?.message || 'The request could not be completed.', response.status, code)
}

export function mutate<T>(path: string, method: 'POST' | 'PATCH' | 'PUT' | 'DELETE', body?: unknown, options: RequestOptions = {}) {
  return request<T>(path, { method, body: body === undefined ? undefined : JSON.stringify(body) }, options)
}
