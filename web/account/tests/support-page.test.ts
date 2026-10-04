import { cleanup, fireEvent, render, screen } from '@testing-library/svelte'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import type { SupportTicketDetail, SupportTicketSummary } from '../src/lib/support'

const support = vi.hoisted(() => ({
  getSupportTickets: vi.fn(),
  getSupportTicket: vi.fn(),
  closeSupportTicket: vi.fn(),
  reopenSupportTicket: vi.fn(),
  replyToSupportTicket: vi.fn(),
  submitSupportRequest: vi.fn(),
  getSupportMetadata: vi.fn(),
}))

const auth = vi.hoisted(() => ({
  getNotificationPreferences: vi.fn(),
}))

vi.mock('../src/lib/support', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../src/lib/support')>()
  return { ...actual, ...support }
})
vi.mock('../src/lib/auth', () => auth)
vi.mock('../src/lib/runtime-config', () => ({ siteOrigin: 'https://website.test.invalid', apiBaseURL: 'https://api.test.invalid' }))

import SupportPage from '../src/pages/SupportPage.svelte'

const account = { id: 'account-test', email: 'tester@example.invalid', emailVerified: true, betaAccess: true }

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

afterEach(() => {
  cleanup()
  support.getSupportTickets.mockReset()
  support.getSupportTicket.mockReset()
  support.closeSupportTicket.mockReset()
  support.reopenSupportTicket.mockReset()
  support.replyToSupportTicket.mockReset()
  support.submitSupportRequest.mockReset()
  support.getSupportMetadata.mockReset()
  auth.getNotificationPreferences.mockReset()
  window.history.replaceState({}, '', '/support')
})

function mockEmailExpectations(receiptEmail = true, supportReplies = true) {
  support.getSupportMetadata.mockResolvedValue({ status: 'private-beta', url: 'https://website.test.invalid/support', intake: '/v1/support/requests', attachmentsAccepted: false, receiptEmail })
  auth.getNotificationPreferences.mockResolvedValue({ betaReleases: true, supportReplies, productAnnouncements: false })
  support.getSupportTickets.mockResolvedValue([])
}

beforeEach(() => {
  mockEmailExpectations()
})

test('tells a guest that a receipt is emailed when mail is configured', async () => {
  mockEmailExpectations(true)
  render(SupportPage, { account: null })
  expect(await screen.findByText('We will email a receipt to this address. Guest requests do not receive reply email.')).toBeTruthy()
})

test('tells a signed-in requester that reply email is on and links to the setting', async () => {
  mockEmailExpectations(true, true)
  render(SupportPage, { account })
  expect(await screen.findByText('We will email a receipt, and replies from support will be emailed to tester@example.invalid.', { exact: false })).toBeTruthy()
  expect(screen.getByRole('link', { name: 'Change your reply email setting' }).getAttribute('href')).toBe('/account#security')
})

test('tells a signed-in requester when reply email is turned off', async () => {
  mockEmailExpectations(true, false)
  render(SupportPage, { account })
  expect(await screen.findByText('We will email a receipt. Reply email is turned off for this account.', { exact: false })).toBeTruthy()
  expect(screen.getByRole('link', { name: 'Change your reply email setting' }).getAttribute('href')).toBe('/account#security')
})

test('says email is unavailable when the deployment has no mail configured', async () => {
  mockEmailExpectations(false)
  render(SupportPage, { account: null })
  expect(await screen.findByText('Email is unavailable on this deployment, so no receipt will be sent. Keep the reference shown after you send.')).toBeTruthy()
  expect(screen.queryByRole('link', { name: 'Change your reply email setting' })).toBeNull()
})

test('blocks secret-shaped intake and accepts a clean guest request', async () => {
  support.submitSupportRequest.mockResolvedValue({ requestId: 'req-test', status: 'open' })
  render(SupportPage, { account: null })
  expect(screen.getByText('Want a request history?')).toBeTruthy()
  await fireEvent.input(screen.getByLabelText('Email'), { target: { value: 'tester@example.invalid' } })
  await fireEvent.input(screen.getByLabelText('Short title'), { target: { value: 'Cannot sign in' } })
  const message = screen.getByLabelText('Message')
  await fireEvent.input(message, { target: { value: 'password: hunter2' } })
  expect(screen.getByText('This looks like a password-shaped value. Remove it before sending.')).toBeTruthy()
  expect((screen.getByRole('button', { name: 'Send request' }) as HTMLButtonElement).disabled).toBe(true)
  await fireEvent.input(message, { target: { value: 'I cannot sign in after the update and need help.' } })
  await fireEvent.click(screen.getByRole('button', { name: 'Send request' }))
  expect(await screen.findByText('Request req-test')).toBeTruthy()
  expect(support.submitSupportRequest).toHaveBeenCalledWith(expect.objectContaining({
    email: 'tester@example.invalid',
    subject: 'Cannot sign in',
    message: 'I cannot sign in after the update and need help.',
    category: 'general',
  }))
})

test('loads the account history and opens a conversation', async () => {
  support.getSupportTickets.mockResolvedValue([ticketSummary({ unreadCount: 2 })])
  support.getSupportTicket.mockResolvedValue(ticketDetail({
    messages: [
      { id: 'message-test', authorRole: 'user', body: 'I cannot sign in after the update.', createdAt: '2026-08-30T09:00:00Z' },
      { id: 'message-staff', authorRole: 'staff', body: 'A fix is on the way.', createdAt: '2026-08-31T11:00:00Z' },
    ],
  }))
  render(SupportPage, { account })
  expect(await screen.findByText('Signed in as tester@example.invalid')).toBeTruthy()
  expect(await screen.findByLabelText('2 unread support replies')).toBeTruthy()
  await fireEvent.click(screen.getByRole('button', { name: /Cannot sign in/ }))
  expect(await screen.findByText('A fix is on the way.')).toBeTruthy()
  expect(screen.getByText('I cannot sign in after the update.')).toBeTruthy()
  expect(support.getSupportTicket).toHaveBeenCalledWith('ticket-test')
  expect(screen.getByRole('button', { name: 'Close request' })).toBeTruthy()
})

test('closes a request and offers the reopen path', async () => {
  const open = ticketDetail()
  const closed = ticketDetail({ status: 'closed', canClose: false, canReopen: true })
  support.getSupportTickets.mockResolvedValue([ticketSummary()])
  support.getSupportTicket.mockResolvedValueOnce(open).mockResolvedValue(closed)
  support.closeSupportTicket.mockResolvedValue(closed)
  render(SupportPage, { account })
  await fireEvent.click(await screen.findByRole('button', { name: /Cannot sign in/ }))
  await fireEvent.click(await screen.findByRole('button', { name: 'Close request' }))
  expect(support.closeSupportTicket).toHaveBeenCalledWith('ticket-test')
  expect(await screen.findByText('This request is closed. You can reopen it for 30 days, then start a new request if the problem returned.')).toBeTruthy()
  expect(await screen.findByRole('button', { name: 'Reopen request' })).toBeTruthy()
})

test('says a request closed automatically after 14 days without a reply', async () => {
  const closed = ticketDetail({ status: 'closed', autoClosed: true, canClose: false, canReopen: true })
  support.getSupportTickets.mockResolvedValue([ticketSummary({ status: 'closed', autoClosed: true, canClose: false, canReopen: true })])
  support.getSupportTicket.mockResolvedValue(closed)
  render(SupportPage, { account })
  await fireEvent.click(await screen.findByRole('button', { name: /Cannot sign in/ }))
  expect(await screen.findByText('This request closed automatically after 14 days without a reply. You can reopen it for 30 days, then start a new request if the problem returned.')).toBeTruthy()
  expect(screen.getByRole('button', { name: 'Reopen request' })).toBeTruthy()
})

test('shows the 3-business-day response expectation on the form', async () => {
  render(SupportPage, { account: null })
  expect(await screen.findByText('We aim to reply within 3 business days.')).toBeTruthy()
})

test('blocks secret-shaped replies and sends a clean follow-up', async () => {
  const replied = ticketDetail({
    status: 'waiting',
    messages: [
      { id: 'message-test', authorRole: 'user', body: 'I cannot sign in after the update.', createdAt: '2026-08-30T09:00:00Z' },
      { id: 'message-reply', authorRole: 'staff', body: 'A fix is on the way.', createdAt: '2026-08-31T11:00:00Z' },
    ],
  })
  support.getSupportTickets.mockResolvedValue([ticketSummary()])
  support.getSupportTicket.mockResolvedValueOnce(ticketDetail()).mockResolvedValue(replied)
  support.replyToSupportTicket.mockResolvedValue(replied)
  render(SupportPage, { account })
  await fireEvent.click(await screen.findByRole('button', { name: /Cannot sign in/ }))
  const reply = await screen.findByLabelText('Add a follow-up')
  await fireEvent.input(reply, { target: { value: 'otpauth://totp/Sesame:tester?secret=JBSWY3DPEHPK3PXP' } })
  expect(screen.getByText('This looks like an authenticator setup link. Remove it before sending.')).toBeTruthy()
  expect((screen.getByRole('button', { name: 'Send follow-up' }) as HTMLButtonElement).disabled).toBe(true)
  await fireEvent.input(reply, { target: { value: 'Thanks, the new build works now.' } })
  await fireEvent.click(screen.getByRole('button', { name: 'Send follow-up' }))
  expect(support.replyToSupportTicket).toHaveBeenCalledWith('ticket-test', 'Thanks, the new build works now.')
  expect(await screen.findByText('A fix is on the way.')).toBeTruthy()
})

test('prefills the Sync interest request from the query string', async () => {
  window.history.replaceState({}, '', '/support?intent=sync')
  render(SupportPage, { account: null })
  expect((screen.getByLabelText('Short title') as HTMLInputElement).value).toBe('Sesame Sync interest')
  expect((screen.getByLabelText('Message') as HTMLTextAreaElement).value).toBe('Please tell me when Sesame Sync is available.')
  expect((screen.getByLabelText('Topic') as HTMLSelectElement).value).toBe('billing')
})

test('prefills category and technical details from the query string', async () => {
  window.history.replaceState({}, '', '/support?category=bug&appVersion=0.1.0-beta.3&diagnosticCode=UI-7F2A')
  render(SupportPage, { account: null })
  expect((screen.getByLabelText('Topic') as HTMLSelectElement).value).toBe('bug')
  expect((screen.getByLabelText('App version') as HTMLInputElement).value).toBe('0.1.0-beta.3')
  expect((screen.getByLabelText('Diagnostic code') as HTMLInputElement).value).toBe('UI-7F2A')
})
