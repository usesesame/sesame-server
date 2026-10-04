import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import type { SupportTicketDetail, SupportTicketSummary } from '../src/lib/support'

const fetchMock = vi.fn()
vi.stubGlobal('fetch', fetchMock)

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

function ticketSummary(overrides: Partial<SupportTicketSummary> = {}): SupportTicketSummary {
  return {
    id: 'ticket-test',
    subject: 'Cannot sign in',
    status: 'open',
    category: 'account',
    appVersion: '0.1.0-beta.3',
    diagnosticCode: 'UI-7F2A',
    browserIntegration: 'ready',
    requestId: 'req-1234',
    messageCount: 1,
    unreadCount: 0,
    createdAt: '2026-08-30T09:00:00Z',
    updatedAt: '2026-08-31T10:00:00Z',
    autoClosed: false,
    canClose: true,
    canReopen: false,
    ...overrides,
  }
}

function ticketDetail(overrides: Partial<SupportTicketDetail> = {}): SupportTicketDetail {
  return {
    ...ticketSummary(),
    messages: [{ id: 'message-test', authorRole: 'user', body: 'I cannot sign in after the update.', createdAt: '2026-08-30T09:00:00Z' }],
    ...overrides,
  }
}

beforeEach(() => {
  vi.resetModules()
})

afterEach(() => {
  fetchMock.mockReset()
})

test.each([
  ['otpauth://totp/Sesame:tester@example.invalid?secret=JBSWY3DPEHPK3PXP', 'an authenticator setup link'],
  ['-----BEGIN PRIVATE KEY-----', 'a private key'],
  ['password: hunter2', 'a password-shaped value'],
  ['totp code: 123456', 'a 2FA code'],
  ['backup code: ABCD-EFGH', 'a recovery code'],
  ['api key = abcdefghijklmnop', 'a token or secret'],
  ['JBSWY3DPEHPK3PXPJBSWY3DP', 'a long authenticator-style secret'],
])('flags %j as %s', async (text, label) => {
  const { findSecretShapedText } = await import('../src/lib/support')
  expect(findSecretShapedText(text)).toBe(label)
})

test('clears ordinary support text', async () => {
  const { findSecretShapedText } = await import('../src/lib/support')
  expect(findSecretShapedText('The app crashes when I click Import on Windows 11.')).toBeNull()
  expect(findSecretShapedText('My version is 0.1.0-beta.3 and the diagnostic code is UI-7F2A.')).toBeNull()
})

test('maps categories and falls back to General', async () => {
  const { supportCategoryLabel } = await import('../src/lib/support')
  expect(supportCategoryLabel('browser_helper')).toBe('Browser helper')
  expect(supportCategoryLabel('something-unknown')).toBe('General')
})

test('rejects secret-shaped intake before any request', async () => {
  const { submitSupportRequest } = await import('../src/lib/support')
  await expect(submitSupportRequest({
    email: 'tester@example.invalid',
    subject: 'Cannot sign in',
    message: 'password: hunter2',
  })).rejects.toThrow('Remove a password-shaped value before sending. Sesame support cannot receive secrets.')
  expect(fetchMock).not.toHaveBeenCalled()
})

test('posts an intake request and returns the receipt', async () => {
  fetchMock.mockImplementation(async (url: string) => url.endsWith('/v1/auth/csrf')
    ? jsonResponse({ token: 'csrf-test' })
    : jsonResponse({ requestId: 'req-test', status: 'open' }))
  const { submitSupportRequest } = await import('../src/lib/support')
  await expect(submitSupportRequest({
    email: 'tester@example.invalid',
    subject: 'Cannot sign in',
    message: 'I cannot sign in after the update.',
    category: 'account',
    appVersion: '0.1.0-beta.3',
  })).resolves.toEqual({ requestId: 'req-test', status: 'open' })
  const [url, init] = fetchMock.mock.calls[1]
  expect(url).toBe('https://api.test.invalid/v1/support/requests')
  expect(init.method).toBe('POST')
  expect(JSON.parse(init.body)).toEqual({
    email: 'tester@example.invalid',
    subject: 'Cannot sign in',
    message: 'I cannot sign in after the update.',
    category: 'account',
    appVersion: '0.1.0-beta.3',
  })
})

test('lists the signed-in account tickets', async () => {
  fetchMock.mockResolvedValueOnce(jsonResponse({ tickets: [ticketSummary()] }))
  const { getSupportTickets } = await import('../src/lib/support')
  await expect(getSupportTickets()).resolves.toEqual([ticketSummary()])
  expect(fetchMock).toHaveBeenCalledWith('https://api.test.invalid/v1/account/support', expect.objectContaining({ credentials: 'include' }))
})

test('loads one ticket by id', async () => {
  fetchMock.mockResolvedValueOnce(jsonResponse({ ticket: ticketDetail() }))
  const { getSupportTicket } = await import('../src/lib/support')
  await expect(getSupportTicket('ticket-test')).resolves.toEqual(ticketDetail())
  expect(fetchMock).toHaveBeenCalledWith('https://api.test.invalid/v1/account/support/ticket-test', expect.objectContaining({ credentials: 'include' }))
})

test('rejects a secret-shaped reply before any request', async () => {
  const { replyToSupportTicket } = await import('../src/lib/support')
  await expect(replyToSupportTicket('ticket-test', 'backup code: ABCD-EFGH')).rejects.toThrow('Remove a recovery code before sending.')
  expect(fetchMock).not.toHaveBeenCalled()
})

test('posts a reply and returns the updated ticket', async () => {
  const replied = ticketDetail({
    status: 'waiting',
    messages: [
      { id: 'message-test', authorRole: 'user', body: 'I cannot sign in after the update.', createdAt: '2026-08-30T09:00:00Z' },
      { id: 'message-reply', authorRole: 'staff', body: 'A fix is on the way.', createdAt: '2026-08-31T11:00:00Z' },
    ],
  })
  fetchMock.mockImplementation(async (url: string) => url.endsWith('/v1/auth/csrf')
    ? jsonResponse({ token: 'csrf-test' })
    : jsonResponse({ ticket: replied }))
  const { replyToSupportTicket } = await import('../src/lib/support')
  await expect(replyToSupportTicket('ticket-test', 'A fix is on the way.')).resolves.toEqual(replied)
  const [url, init] = fetchMock.mock.calls[1]
  expect(url).toBe('https://api.test.invalid/v1/account/support/ticket-test/reply')
  expect(init.method).toBe('POST')
  expect(JSON.parse(init.body)).toEqual({ message: 'A fix is on the way.' })
})

test('closes and reopens a ticket through the lifecycle routes', async () => {
  fetchMock.mockImplementation(async (url: string) => url.endsWith('/v1/auth/csrf')
    ? jsonResponse({ token: 'csrf-test' })
    : jsonResponse({ ticket: ticketDetail() }))
  const { closeSupportTicket, reopenSupportTicket } = await import('../src/lib/support')
  await expect(closeSupportTicket('ticket-test')).resolves.toEqual(ticketDetail())
  await expect(reopenSupportTicket('ticket-test')).resolves.toEqual(ticketDetail())
  expect(fetchMock.mock.calls[1][0]).toBe('https://api.test.invalid/v1/account/support/ticket-test/close')
  expect(fetchMock.mock.calls[1][1].method).toBe('POST')
  expect(fetchMock.mock.calls[2][0]).toBe('https://api.test.invalid/v1/account/support/ticket-test/reopen')
  expect(fetchMock.mock.calls[2][1].method).toBe('POST')
})

test('redeems a guest link with the CSRF header and no session requirement', async () => {
  fetchMock.mockImplementation(async (url: string) => url.endsWith('/v1/auth/csrf')
    ? jsonResponse({ token: 'csrf-test' })
    : jsonResponse({ ticket: ticketDetail() }))
  const { openSupportAccess } = await import('../src/lib/support')
  await expect(openSupportAccess('guest-token-test')).resolves.toEqual(ticketDetail())
  const [url, init] = fetchMock.mock.calls[1]
  expect(url).toBe('https://api.test.invalid/v1/support/access')
  expect(init.method).toBe('POST')
  expect(JSON.parse(init.body)).toEqual({ token: 'guest-token-test' })
  expect(new Headers(init.headers).get('X-Sesame-CSRF')).toBe('csrf-test')
})

test('replies through a guest link and attaches it to the account', async () => {
  fetchMock.mockImplementation(async (url: string) => url.endsWith('/v1/auth/csrf')
    ? jsonResponse({ token: 'csrf-test' })
    : jsonResponse({ ticket: ticketDetail() }))
  const { attachSupportTicket, replyToSupportAccess } = await import('../src/lib/support')
  await expect(replyToSupportAccess('guest-token-test', 'A fictional follow-up.')).resolves.toEqual(ticketDetail())
  await expect(attachSupportTicket('ticket-test')).resolves.toEqual(ticketDetail())
  expect(fetchMock.mock.calls[1][0]).toBe('https://api.test.invalid/v1/support/access/reply')
  expect(JSON.parse(fetchMock.mock.calls[1][1].body)).toEqual({ token: 'guest-token-test', message: 'A fictional follow-up.' })
  expect(fetchMock.mock.calls[2][0]).toBe('https://api.test.invalid/v1/account/support/ticket-test/attach')
  expect(fetchMock.mock.calls[2][1].method).toBe('POST')
})

test('rejects a secret-shaped guest reply before any request', async () => {
  const { replyToSupportAccess } = await import('../src/lib/support')
  await expect(replyToSupportAccess('guest-token-test', 'password: hunter2')).rejects.toThrow('Remove a password-shaped value before sending.')
  expect(fetchMock).not.toHaveBeenCalled()
})
