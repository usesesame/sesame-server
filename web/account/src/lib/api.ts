import { apiBaseURL } from './runtime-config'

const REQUEST_TIMEOUT_MS = 8_000

type ErrorResponse = { error?: { code?: string; message?: string } }

/** A safe, machine-readable error returned by the account API or transport. */
export class ApiError extends Error {
  readonly code: string
  readonly status: number
  readonly retryAfter?: number
  readonly requestId?: string

  constructor(message: string, options: { code: string; status: number; retryAfter?: number; requestId?: string }) {
    super(message)
    this.name = 'ApiError'
    this.code = options.code
    this.status = options.status
    this.retryAfter = options.retryAfter
    this.requestId = options.requestId
  }
}

export const API_ROUTES = {
  csrf: '/v1/auth/csrf',
  me: '/v1/auth/me',
  login: '/v1/auth/login',
  logout: '/v1/auth/logout',
  registration: '/v1/auth/registration',
  register: '/v1/auth/register',
  requestEmailVerification: '/v1/auth/email/verification/request',
  confirmEmailVerification: '/v1/auth/email/verification/confirm',
  requestPasswordRecovery: '/v1/auth/password/recovery/request',
  confirmPasswordRecovery: '/v1/auth/password/recovery/confirm',
  reauthenticate: '/v1/account/reauthenticate',
  password: '/v1/account/password',
  emailChangeRequest: '/v1/account/email/change/request',
  emailChangeConfirm: '/v1/account/email/change/confirm',
  sessions: '/v1/account/sessions',
  desktopLink: '/v1/account/desktop-link',
  devices: '/v1/account/devices',
  access: '/v1/account/access',
  bootstrap: '/v1/account/bootstrap',
  activity: '/v1/account/activity',
  notifications: '/v1/account/notifications',
  downloads: '/v1/account/downloads',
	downloadTickets: '/v1/account/download-tickets',
  supportRequests: '/v1/support/requests',
	accountSupport: '/v1/account/support',
} as const

const apiBase = apiBaseURL

let csrfToken = ''
let csrfRequest: Promise<string> | null = null

export async function apiRequest(path: string, init: RequestInit = {}): Promise<Response> {
	const method = (init.method || 'GET').toUpperCase()
	const protectedMutation = !['GET', 'HEAD', 'OPTIONS'].includes(method)
		&& (path.startsWith('/v1/auth/')
			|| path.startsWith('/v1/account/')
			|| path === API_ROUTES.supportRequests)
	let requestInit = init
	if (protectedMutation) requestInit = withCSRF(init, await getCSRFToken())
	let response = await timedFetch(path, requestInit)
	if (protectedMutation && response.status === 403 && await isInvalidCSRF(response)) {
		csrfToken = ''
		response = await timedFetch(path, withCSRF(init, await getCSRFToken()))
	}
	return response
}

async function timedFetch(path: string, init: RequestInit): Promise<Response> {
  const controller = new AbortController()
  const timeout = window.setTimeout(() => controller.abort(), REQUEST_TIMEOUT_MS)

  try {
    return await fetch(`${apiBase}${path}`, {
      ...init,
      credentials: 'include',
      signal: controller.signal,
    })
  } catch (reason) {
    if (reason instanceof DOMException && reason.name === 'AbortError') {
      throw new ApiError('The Sesame account service did not respond. Please try again.', { code: 'timeout', status: 0 })
    }
    const offline = typeof navigator !== 'undefined' && !navigator.onLine
    throw new ApiError(
      offline ? 'You appear to be offline. Please reconnect and try again.' : 'The Sesame account service could not be reached. Please try again.',
      { code: offline ? 'offline' : 'network_error', status: 0 },
    )
  } finally {
    window.clearTimeout(timeout)
  }
}

/** Convert a non-success response into the error shape used throughout the website. */
export async function responseError(response: Response): Promise<ApiError> {
  const body = await response.json().catch(() => null) as ErrorResponse | null
  const retryAfter = retryAfterSeconds(response.headers.get('Retry-After'))
  const code = body?.error?.code || (response.status >= 500 ? 'service_unavailable' : 'request_failed')
  const message = body?.error?.message || 'The account service is temporarily unavailable.'
  return new ApiError(message, {
    code,
    status: response.status,
    retryAfter,
    requestId: response.headers.get('X-Request-ID') || undefined,
  })
}

function retryAfterSeconds(value: string | null): number | undefined {
  if (!value) return undefined
  const seconds = Number(value)
  if (Number.isFinite(seconds) && seconds >= 0) return seconds
  const timestamp = Date.parse(value)
  return Number.isNaN(timestamp) ? undefined : Math.max(0, Math.ceil((timestamp - Date.now()) / 1000))
}

function withCSRF(init: RequestInit, token: string): RequestInit {
	const headers = new Headers(init.headers)
	headers.set('X-Sesame-CSRF', token)
	return { ...init, headers }
}

async function getCSRFToken(): Promise<string> {
	if (csrfToken) return csrfToken
	if (!csrfRequest) {
		csrfRequest = timedFetch(API_ROUTES.csrf, { method: 'GET' })
			.then(async (response) => {
				if (!response.ok) throw await responseError(response)
				const body = await response.json() as { token?: string }
				if (!body.token) throw new ApiError('The Sesame account security check returned an invalid response.', { code: 'invalid_response', status: response.status })
				csrfToken = body.token
				return csrfToken
			})
			.finally(() => { csrfRequest = null })
	}
	return csrfRequest
}

async function isInvalidCSRF(response: Response): Promise<boolean> {
	const body = await response.clone().json().catch(() => null) as { error?: { code?: string } } | null
	return body?.error?.code === 'invalid_csrf'
}
