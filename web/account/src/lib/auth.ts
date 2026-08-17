import { API_ROUTES, ApiError, apiRequest, responseError } from './api'
import { apiBaseURL } from './runtime-config'

export interface Account {
  id: string
  email: string
  emailVerified: boolean
  betaAccess: boolean
}

export type RegistrationStatus = {
  mode: 'closed' | 'invite' | 'public'
  enabled: boolean
  requiresInvite: boolean
}

export type LegalAcceptance = {
  termsAccepted: boolean
  termsVersion: string
  privacyAcknowledged: boolean
  privacyVersion: string
}

export type DesktopLink = {
  state: 'none' | 'pending' | 'connected' | 'expired'
  linkId?: string
  code?: string
  expiresAt?: string
  deviceId?: string
}

export type DesktopDevice = {
  deviceId: string
  deviceName: string
  connectedAt: string
  expiresAt: string
  appVersion?: string
  platform?: string
  architecture?: string
  updateChannel?: string
  lastSeenAt: string
  protocolVersion: number
  browserHelperCapable: boolean
  browserHelperLastObservedAt?: string
}

export type AccountSession = {
  id: string
  label: string
  createdAt: string
  lastSeenAt: string
  authenticatedAt: string
  expiresAt: string
  current: boolean
}

export type AccountAccess = {
  betaAccess: boolean
  emailVerified: boolean
  downloadsAllowed: boolean
  licences: Array<{ id: string; product: string; status: string; issuedAt: string; expiresAt?: string; graceEndsAt?: string }>
}

export type AccountDownload = {
	id: string
	version: string
	platform: string
  sha256: string
  updaterVerified: boolean
  distributionClass: 'lab' | 'early_access' | 'production'
  sigstoreVerified: boolean
  sigstoreIdentity?: string
  authenticodeVerified: boolean
  signature: string
  signingKeyId: string
  supportedWindows: string
  releaseNotesUrl: string
  rollbackNotice?: string
  publishedAt?: string
}

export type AccountBootstrap = {
  account: Account
  access: AccountAccess
  licences: AccountAccess['licences']
  capabilities: {
    desktopLinking: boolean
    passkeys: boolean
    browserHelper: boolean
    notifications: boolean
  }
  notificationCounts: { security: number; support: number; product: number }
  security: { activeSessions: number; connectedDesktops: number; recentAuthenticationAt: string }
}

export type AccountActivityEvent = {
  id: string
  type: string
  label: string
  metadata?: Record<string, string>
  createdAt: string
  expiresAt: string
}

export type NotificationPreferences = {
  betaReleases: boolean
  supportReplies: boolean
  productAnnouncements: boolean
}

type AuthResponse = { user: Account }
let bootstrapETag = ''
let bootstrapCache: AccountBootstrap | null = null
export type AuthState =
  | { state: 'loading' }
  | { state: 'authenticated'; account: Account }
  | { state: 'anonymous'; expired?: boolean }
  | { state: 'offline'; account?: Account }
  | { state: 'error'; error: ApiError }

export async function loadAuthState(previousAccount?: Account): Promise<Exclude<AuthState, { state: 'loading' }>> {
  try {
    const response = await apiRequest(API_ROUTES.me)
    if (response.status === 401) {
      const error = await responseError(response)
      return { state: 'anonymous', expired: error.code === 'session_expired' }
    }
    if (!response.ok) return { state: 'error', error: await responseError(response) }
    return { state: 'authenticated', account: (await response.json() as AuthResponse).user }
  } catch (reason) {
    if (reason instanceof ApiError && ['offline', 'timeout', 'network_error'].includes(reason.code)) {
      return previousAccount ? { state: 'offline', account: previousAccount } : { state: 'offline' }
    }
    const error = reason instanceof ApiError
      ? reason
      : new ApiError('The account service is temporarily unavailable.', { code: 'service_unavailable', status: 0 })
    return { state: 'error', error }
  }
}

// Retained for callers that only need the account. Unlike the previous version,
// an API outage rejects instead of being mistaken for an anonymous session.
export async function currentAccount(): Promise<Account | null> {
  const state = await loadAuthState()
  if (state.state === 'authenticated') return state.account
  if (state.state === 'anonymous') return null
  if (state.state === 'error') throw state.error
  throw new ApiError('The account service could not be reached.', { code: state.state, status: 0 })
}

export async function getRegistrationStatus(): Promise<RegistrationStatus> {
  const response = await apiRequest(API_ROUTES.registration)
  if (!response.ok) throw await requestError(response)
  return await response.json() as RegistrationStatus
}

export async function register(email: string, password: string, inviteCode: string | undefined, legal: LegalAcceptance): Promise<Account> {
  const response = await apiRequest(API_ROUTES.register, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ email, password, ...legal, ...(inviteCode ? { inviteCode } : {}) }),
  })
  if (!response.ok) throw await requestError(response)
  return (await response.json() as AuthResponse).user
}

export async function signIn(email: string, password: string): Promise<Account> {
  const response = await apiRequest(API_ROUTES.login, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ email, password }),
  })
  if (!response.ok) throw await requestError(response)
  return (await response.json() as AuthResponse).user
}

export async function signOut(): Promise<void> {
  const response = await apiRequest(API_ROUTES.logout, { method: 'POST' })
  if (!response.ok && response.status !== 401) throw await requestError(response)
}

export async function requestEmailVerification(): Promise<void> {
  const response = await apiRequest(API_ROUTES.requestEmailVerification, { method: 'POST' })
  if (!response.ok) throw await requestError(response)
}

export async function confirmEmailVerification(token: string): Promise<Account> {
  const response = await apiRequest(API_ROUTES.confirmEmailVerification, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ token }),
  })
  if (!response.ok) throw await requestError(response)
  return (await response.json() as AuthResponse).user
}

export async function requestPasswordRecovery(email: string): Promise<void> {
  const response = await apiRequest(API_ROUTES.requestPasswordRecovery, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ email }),
  })
  if (!response.ok) throw await requestError(response)
}

export async function confirmPasswordRecovery(token: string, newPassword: string): Promise<Account> {
  const response = await apiRequest(API_ROUTES.confirmPasswordRecovery, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ token, newPassword }),
  })
  if (!response.ok) throw await requestError(response)
  return (await response.json() as AuthResponse).user
}

export async function reauthenticate(password: string): Promise<void> {
  const response = await apiRequest(API_ROUTES.reauthenticate, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ password }),
  })
  if (!response.ok) throw await requestError(response)
}

export async function requestEmailChange(newEmail: string): Promise<void> {
  const response = await apiRequest(API_ROUTES.emailChangeRequest, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ newEmail }),
  })
  if (!response.ok) throw await requestError(response)
}

export async function confirmEmailChange(token: string): Promise<Account> {
  const response = await apiRequest(API_ROUTES.emailChangeConfirm, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ token }),
  })
  if (!response.ok) throw await requestError(response)
  return (await response.json() as AuthResponse).user
}

export async function listSessions(): Promise<AccountSession[]> {
  const response = await apiRequest(API_ROUTES.sessions)
  if (!response.ok) throw await requestError(response)
  return (await response.json() as { sessions?: AccountSession[] }).sessions ?? []
}

export async function revokeSession(id: string): Promise<void> {
  const response = await apiRequest(`${API_ROUTES.sessions}/${encodeURIComponent(id)}`, { method: 'DELETE' })
  if (!response.ok) throw await requestError(response)
}

export async function revokeAllSessions(): Promise<void> {
  const response = await apiRequest(API_ROUTES.sessions, { method: 'DELETE' })
  if (!response.ok) throw await requestError(response)
}

export async function getDesktopLink(): Promise<DesktopLink> {
  const response = await apiRequest(API_ROUTES.desktopLink)
  if (!response.ok) throw await requestError(response)
  return await response.json() as DesktopLink
}

export async function createDesktopLink(): Promise<DesktopLink> {
  const response = await apiRequest(API_ROUTES.desktopLink, { method: 'POST' })
  if (!response.ok) throw await requestError(response)
  return await response.json() as DesktopLink
}

export async function cancelDesktopLink(): Promise<void> {
  const response = await apiRequest(API_ROUTES.desktopLink, { method: 'DELETE' })
  if (!response.ok && response.status !== 404) throw await requestError(response)
}

export async function listDesktopDevices(): Promise<DesktopDevice[]> {
  const response = await apiRequest(API_ROUTES.devices)
  if (!response.ok) throw await requestError(response)
  return (await response.json() as { devices?: DesktopDevice[] }).devices ?? []
}

export async function revokeDesktopDevice(deviceId: string): Promise<void> {
  const response = await apiRequest(`${API_ROUTES.devices}/${encodeURIComponent(deviceId)}`, { method: 'DELETE' })
  if (!response.ok) throw await requestError(response)
}

export async function renameDesktopDevice(deviceId: string, deviceName: string): Promise<void> {
  const response = await apiRequest(`${API_ROUTES.devices}/${encodeURIComponent(deviceId)}`, {
    method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ deviceName }),
  })
  if (!response.ok) throw await requestError(response)
}

export async function getAccountAccess(): Promise<AccountAccess> {
  const response = await apiRequest(API_ROUTES.access)
  if (!response.ok) throw await requestError(response)
  return await response.json() as AccountAccess
}

export async function getAccountBootstrap(): Promise<AccountBootstrap> {
  const headers = new Headers()
  if (bootstrapETag) headers.set('If-None-Match', bootstrapETag)
  const response = await apiRequest(API_ROUTES.bootstrap, { headers })
  if (response.status === 304 && bootstrapCache) return bootstrapCache
  if (!response.ok) throw await requestError(response)
  bootstrapETag = response.headers.get('ETag') || ''
  bootstrapCache = await response.json() as AccountBootstrap
  return bootstrapCache
}

export async function getAccountActivity(): Promise<AccountActivityEvent[]> {
  const response = await apiRequest(API_ROUTES.activity)
  if (!response.ok) throw await requestError(response)
  return (await response.json() as { events?: AccountActivityEvent[] }).events ?? []
}

export async function getNotificationPreferences(): Promise<NotificationPreferences> {
  const response = await apiRequest(API_ROUTES.notifications)
  if (!response.ok) throw await requestError(response)
  return (await response.json() as { preferences: NotificationPreferences }).preferences
}

export async function updateNotificationPreferences(preferences: NotificationPreferences): Promise<void> {
  const response = await apiRequest(API_ROUTES.notifications, {
    method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(preferences),
  })
  if (!response.ok) throw await requestError(response)
}

export async function getAccountDownloads(): Promise<AccountDownload[]> {
  const response = await apiRequest(API_ROUTES.downloads)
  if (!response.ok) throw await requestError(response)
  return (await response.json() as { releases?: AccountDownload[] }).releases ?? []
}

export async function createDownloadTicket(release: Pick<AccountDownload, 'id' | 'platform'>): Promise<string> {
  const idempotencyKey = newIdempotencyKey()
  const response = await apiRequest(API_ROUTES.downloadTickets, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'Idempotency-Key': idempotencyKey },
    body: JSON.stringify({ releaseId: release.id, platform: release.platform }),
  })
  if (!response.ok) throw await requestError(response)
  const payload = await response.json() as { downloadUrl?: string }
  if (!payload.downloadUrl || !payload.downloadUrl.startsWith('/v1/downloads/')) {
    throw new ApiError('The download service returned an invalid ticket.', { code: 'invalid_download_ticket', status: response.status })
  }
  return new URL(payload.downloadUrl, apiBaseURL).toString()
}

function newIdempotencyKey() {
  const bytes = new Uint8Array(24)
  crypto.getRandomValues(bytes)
  return Array.from(bytes, (value) => value.toString(16).padStart(2, '0')).join('')
}

export async function changePassword(currentPassword: string, newPassword: string): Promise<void> {
  const response = await apiRequest(API_ROUTES.password, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ currentPassword, newPassword }),
  })
  if (!response.ok) throw await requestError(response)
}

export async function deleteAccount(password: string): Promise<void> {
  const response = await apiRequest('/v1/account/delete', {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ password }),
  })
  if (!response.ok) throw await requestError(response)
}

export const requestError = responseError
