import { cleanup, fireEvent, render, screen } from '@testing-library/svelte'
import { afterEach, expect, test, vi } from 'vitest'
import { ApiError } from '../src/lib/api'
import type { SupportTicketDetail } from '../src/lib/support'

const support = vi.hoisted(() => ({
  openSupportAccess: vi.fn(),
  replyToSupportAccess: vi.fn(),
  attachSupportTicket: vi.fn(),
}))

vi.mock('../src/lib/support', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../src/lib/support')>()
  return { ...actual, ...support }
})
vi.mock('../src/lib/runtime-config', () => ({ siteOrigin: 'https://website.test.invalid', apiBaseURL: 'https://api.test.invalid' }))

import SupportRequestPage from '../src/pages/SupportRequestPage.svelte'

const PENDING_ATTACH_KEY = 'sesame-support-pending-attach'
const guestToken = 'fictional-guest-token-00000000000000000000'
const account = { id: 'account-test', email: 'tester@example.invalid', emailVerified: true, betaAccess: true }

function ticketDetail(overrides: Partial<SupportTicketDetail> = {}): SupportTicketDetail {
  return {
    id: 'ticket-test',
    subject: 'Cannot sign in',
    status: 'waiting',
    category: 'account',
    appVersion: '0.1.0-beta.3',
    diagnosticCode: 'UI-7F2A',
    browserIntegration: 'ready',
    requestId: 'req-1234',
    messageCount: 2,
    unreadCount: 0,
    createdAt: '2026-08-30T09:00:00Z',
    updatedAt: '2026-08-31T10:00:00Z',
    autoClosed: false,
    canClose: false,
    canReopen: false,
    messages: [
      { id: 'message-test', authorRole: 'user', body: 'I cannot sign in after the update.', createdAt: '2026-08-30T09:00:00Z' },
      { id: 'message-staff', authorRole: 'staff', body: 'A fix is on the way.', createdAt: '2026-08-31T11:00:00Z' },
    ],
    ...overrides,
  }
}

afterEach(() => {
  cleanup()
  support.openSupportAccess.mockReset()
  support.replyToSupportAccess.mockReset()
  support.attachSupportTicket.mockReset()
  sessionStorage.clear()
  vi.unstubAllGlobals()
  window.history.replaceState({}, '', '/support/request')
})

test('shows a loading status and clears the fragment token', async () => {
  let resolveTicket: (value: SupportTicketDetail) => void = () => {}
  support.openSupportAccess.mockImplementation(() => new Promise<SupportTicketDetail>((resolve) => { resolveTicket = resolve }))
  window.history.replaceState({}, '', `/support/request#token=${guestToken}`)
  render(SupportRequestPage)
  expect(screen.getByRole('status').textContent).toContain('Checking this support link')
  expect(window.location.hash).toBe('')
  expect(support.openSupportAccess).toHaveBeenCalledWith(guestToken)
  resolveTicket(ticketDetail())
  expect(await screen.findByRole('heading', { name: 'Cannot sign in' })).toBeTruthy()
})

test('shows the generic inactive-link state with next steps', async () => {
  support.openSupportAccess.mockRejectedValue(new ApiError('That support link is invalid or expired.', { code: 'support_link_invalid', status: 400 }))
  window.history.replaceState({}, '', `/support/request#token=${guestToken}`)
  render(SupportRequestPage)
  const heading = await screen.findByRole('heading', { name: 'This link is no longer active' })
  expect(screen.getByRole('link', { name: 'Send a new request' }).getAttribute('href')).toBe('/support')
  expect(screen.getByRole('link', { name: /sign in/i }).getAttribute('href')).toBe('/login')
  await vi.waitFor(() => expect(document.activeElement).toBe(heading))
})

test('shows the inactive-link state when there is no token at all', async () => {
  render(SupportRequestPage)
  expect(await screen.findByRole('heading', { name: 'This link is no longer active' })).toBeTruthy()
  expect(support.openSupportAccess).not.toHaveBeenCalled()
})

test('renders the guest thread with labeled message articles', async () => {
  support.openSupportAccess.mockResolvedValue(ticketDetail())
  window.history.replaceState({}, '', `/support/request#token=${guestToken}`)
  render(SupportRequestPage)
  expect(await screen.findByRole('heading', { name: 'Cannot sign in' })).toBeTruthy()
  expect(screen.getByText(/Request ticket-test/)).toBeTruthy()
  expect(screen.getByText('Reply sent')).toBeTruthy()
  expect(screen.getByRole('article', { name: 'Sesame support' })).toBeTruthy()
  expect(screen.getByRole('article', { name: 'You' })).toBeTruthy()
  expect(screen.getByText('A fix is on the way.')).toBeTruthy()
})

test('sends a guest follow-up and blocks secret-shaped text', async () => {
  const followedUp = ticketDetail({
    messages: [
      { id: 'message-test', authorRole: 'user', body: 'I cannot sign in after the update.', createdAt: '2026-08-30T09:00:00Z' },
      { id: 'message-staff', authorRole: 'staff', body: 'A fix is on the way.', createdAt: '2026-08-31T11:00:00Z' },
      { id: 'message-follow-up', authorRole: 'user', body: 'The fictional fix worked.', createdAt: '2026-09-01T09:00:00Z' },
    ],
  })
  support.openSupportAccess.mockResolvedValue(ticketDetail())
  support.replyToSupportAccess.mockResolvedValue(followedUp)
  window.history.replaceState({}, '', `/support/request#token=${guestToken}`)
  render(SupportRequestPage)
  const reply = await screen.findByLabelText('Add a follow-up')
  await fireEvent.input(reply, { target: { value: 'password: hunter2' } })
  expect(screen.getByText('This looks like a password-shaped value. Remove it before sending.')).toBeTruthy()
  expect((screen.getByRole('button', { name: 'Send follow-up' }) as HTMLButtonElement).disabled).toBe(true)
  await fireEvent.input(reply, { target: { value: 'The fictional fix worked.' } })
  await fireEvent.click(screen.getByRole('button', { name: 'Send follow-up' }))
  expect(support.replyToSupportAccess).toHaveBeenCalledWith(guestToken, 'The fictional fix worked.')
  expect(await screen.findByText('The fictional fix worked.')).toBeTruthy()
})

test('shows an inline alert and focuses it when a guest reply fails', async () => {
  support.openSupportAccess.mockResolvedValue(ticketDetail())
  support.replyToSupportAccess.mockRejectedValue(new ApiError('That support link is invalid or expired.', { code: 'support_link_invalid', status: 400 }))
  window.history.replaceState({}, '', `/support/request#token=${guestToken}`)
  render(SupportRequestPage)
  const reply = await screen.findByLabelText('Add a follow-up')
  await fireEvent.input(reply, { target: { value: 'The fictional fix worked.' } })
  await fireEvent.click(screen.getByRole('button', { name: 'Send follow-up' }))
  const alert = await screen.findByRole('alert')
  expect(alert.textContent).toContain('That support link is invalid or expired.')
  await vi.waitFor(() => expect(document.activeElement).toBe(alert))
})

test('attaches the request from the guest page for a signed-in account', async () => {
  support.openSupportAccess.mockResolvedValue(ticketDetail())
  support.attachSupportTicket.mockResolvedValue(ticketDetail({ canClose: true }))
  window.history.replaceState({}, '', `/support/request#token=${guestToken}`)
  render(SupportRequestPage, { account })
  expect(await screen.findByRole('heading', { name: 'Keep this request on your account' })).toBeTruthy()
  expect(screen.getByText(/same verified address/)).toBeTruthy()
  await fireEvent.click(screen.getByRole('button', { name: 'Keep this request on your account' }))
  expect(support.attachSupportTicket).toHaveBeenCalledWith('ticket-test')
  expect(await screen.findByText(/This request is now in your account history/)).toBeTruthy()
})

test('keeps only the ticket id across the sign-in round trip when signed out', async () => {
  const assign = vi.fn()
  vi.stubGlobal('location', { assign, search: '', pathname: '/support/request', hash: `#token=${guestToken}`, origin: 'http://localhost', href: 'http://localhost/support/request' })
  support.openSupportAccess.mockResolvedValue(ticketDetail())
  render(SupportRequestPage)
  await screen.findByRole('heading', { name: 'Cannot sign in' })
  await fireEvent.click(screen.getByRole('button', { name: 'Keep this request on your account' }))
  expect(sessionStorage.getItem(PENDING_ATTACH_KEY)).toBe('ticket-test')
  expect(assign).toHaveBeenCalledWith('/login?next=%2Fsupport%2Frequest')
  expect(support.attachSupportTicket).not.toHaveBeenCalled()
})

test('finishes a pending attach after the sign-in round trip and clears it', async () => {
  sessionStorage.setItem(PENDING_ATTACH_KEY, 'ticket-test')
  support.attachSupportTicket.mockResolvedValue(ticketDetail({ canClose: true }))
  render(SupportRequestPage, { account })
  expect(await screen.findByText(/This request is now in your account history/)).toBeTruthy()
  expect(support.attachSupportTicket).toHaveBeenCalledWith('ticket-test')
  expect(sessionStorage.getItem(PENDING_ATTACH_KEY)).toBeNull()
})

test('offers sign-in when a pending attach waits for an anonymous visitor', async () => {
  sessionStorage.setItem(PENDING_ATTACH_KEY, 'ticket-test')
  render(SupportRequestPage)
  expect(await screen.findByRole('heading', { name: 'Sign in to keep this request on your account' })).toBeTruthy()
  expect(screen.getByRole('link', { name: 'Sign in' }).getAttribute('href')).toBe('/login?next=%2Fsupport%2Frequest')
  expect(support.attachSupportTicket).not.toHaveBeenCalled()
})
