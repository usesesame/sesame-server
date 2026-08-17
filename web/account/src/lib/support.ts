import { API_ROUTES, apiRequest, responseError } from './api'

export type SupportCategory = 'general' | 'account' | 'import' | 'sync' | 'browser_helper' | 'billing' | 'bug'

export const SUPPORT_CATEGORIES: Array<{ value: SupportCategory; label: string }> = [
  { value: 'general', label: 'General' },
  { value: 'account', label: 'Account & sign-in' },
  { value: 'import', label: 'Import & migration' },
  { value: 'sync', label: 'Sesame Sync' },
  { value: 'browser_helper', label: 'Browser helper' },
  { value: 'billing', label: 'Billing & licences' },
  { value: 'bug', label: 'Bug report' },
]

export function supportCategoryLabel(value: SupportCategory | string): string {
  return SUPPORT_CATEGORIES.find((category) => category.value === value)?.label ?? 'General'
}

export type SupportRequest = {
  email: string
  subject: string
  message: string
  category?: SupportCategory
  appVersion?: string
  diagnosticCode?: string
  browserIntegration?: string
  requestId?: string
}

export type SupportReceipt = { requestId: string; status: 'open' }

export type SupportTicketStatus = 'open' | 'in_progress' | 'waiting' | 'closed'

export type SupportTicketSummary = {
  id: string
  subject: string
  status: SupportTicketStatus
  category: SupportCategory
  appVersion: string
  diagnosticCode: string
  browserIntegration: string
  requestId: string
  messageCount: number
  unreadCount: number
  createdAt: string
  updatedAt: string
  closedAt?: string
  canClose: boolean
  canReopen: boolean
}

export type SupportTicketMessage = {
  id: string
  authorRole: 'user' | 'staff'
  body: string
  createdAt: string
}

export type SupportTicketDetail = SupportTicketSummary & { messages: SupportTicketMessage[] }

const secretSignals: Array<[RegExp, string]> = [
  [/otpauth:\/\//i, 'an authenticator setup link'],
  [/-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----/i, 'a private key'],
  [/(?:password|passphrase|master\s+password)\s*[:=]\s*\S+/i, 'a password-shaped value'],
  [/(?:totp|otp|2fa)\s*(?:code|secret)?\s*[:=]\s*\d{6,8}/i, 'a 2FA code'],
  [/(?:backup|recovery)\s+code\s*[:=]\s*[A-Z0-9-]{6,}/i, 'a recovery code'],
  [/(?:secret|token|api[_ -]?key)\s*[:=]\s*[A-Za-z0-9_\-/.+=]{12,}/i, 'a token or secret'],
  [/(?:^|\s)[A-Z2-7]{24,}(?:\s|$)/m, 'a long authenticator-style secret'],
]

export function findSecretShapedText(value: string): string | null {
  for (const [pattern, label] of secretSignals) if (pattern.test(value)) return label
  return null
}

export async function submitSupportRequest(input: SupportRequest): Promise<SupportReceipt> {
  const combined = `${input.subject}\n${input.message}`
  const signal = findSecretShapedText(combined)
  if (signal) throw new Error(`Remove ${signal} before sending. Sesame support cannot receive secrets.`)
  const response = await apiRequest(API_ROUTES.supportRequests, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
  })
  if (!response.ok) throw await responseError(response)
  return await response.json() as SupportReceipt
}

export async function getSupportTickets(): Promise<SupportTicketSummary[]> {
  const response = await apiRequest(API_ROUTES.accountSupport)
  if (!response.ok) throw await responseError(response)
  return (await response.json() as { tickets: SupportTicketSummary[] }).tickets
}

export async function getSupportTicket(id: string): Promise<SupportTicketDetail> {
  const response = await apiRequest(`${API_ROUTES.accountSupport}/${encodeURIComponent(id)}`)
  if (!response.ok) throw await responseError(response)
  return (await response.json() as { ticket: SupportTicketDetail }).ticket
}

export async function replyToSupportTicket(id: string, message: string): Promise<SupportTicketDetail> {
  const signal = findSecretShapedText(message)
  if (signal) throw new Error(`Remove ${signal} before sending. Sesame support cannot receive secrets.`)
  const response = await apiRequest(`${API_ROUTES.accountSupport}/${encodeURIComponent(id)}/reply`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ message }),
  })
  if (!response.ok) throw await responseError(response)
  return (await response.json() as { ticket: SupportTicketDetail }).ticket
}

async function supportTicketAction(id: string, action: 'close' | 'reopen'): Promise<SupportTicketDetail> {
  const response = await apiRequest(`${API_ROUTES.accountSupport}/${encodeURIComponent(id)}/${action}`, { method: 'POST' })
  if (!response.ok) throw await responseError(response)
  return (await response.json() as { ticket: SupportTicketDetail }).ticket
}

export function closeSupportTicket(id: string) { return supportTicketAction(id, 'close') }
export function reopenSupportTicket(id: string) { return supportTicketAction(id, 'reopen') }
