import { cleanup, fireEvent, render, screen } from '@testing-library/svelte'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import type { SupportTicketDetail, SupportTicketSummary } from '../src/lib/support'

const support = vi.hoisted(() => ({
  getSupportTickets: vi.fn(),
  getSupportTicket: vi.fn(),
  submitSupportRequest: vi.fn(),
  getSupportMetadata: vi.fn(),
}))
const auth = vi.hoisted(() => ({ getNotificationPreferences: vi.fn() }))

vi.mock('../src/lib/support', async (importOriginal) => ({ ...(await importOriginal<typeof import('../src/lib/support')>()), ...support }))
vi.mock('../src/lib/auth', () => auth)
vi.mock('../src/lib/runtime-config', () => ({ siteOrigin: 'https://website.test.invalid', apiBaseURL: 'https://api.test.invalid' }))
vi.mock('../src/lib/support-answers', () => ({
  HELP_ENTRIES: [
    { id: 'reset', categories: ['account'], keywords: ['forgot password'], title: 'Reset your account password', answer: 'Use the recovery page to choose a new one.', link: { label: 'Recover your account', path: '/forgot-password' } },
  ],
  CATEGORY_GUIDANCE: {
    general: { include: ['What you were trying to do'], placeholder: 'Tell us what happened.' },
    account: { include: ['The email on the account'], placeholder: 'Which page were you on?' },
    import: { include: ['Which app you exported from'], placeholder: 'Which app?' },
    sync: { include: ['What the Sync panel says on each device'], placeholder: 'Which devices?' },
    browser_helper: { include: ['Your browser'], placeholder: 'Which browser?' },
    billing: { include: ['What you tried to buy'], placeholder: 'What did you try?' },
    bug: { include: ['The steps to reproduce'], placeholder: 'What did you do?' },
  },
}))

import SupportPage from '../src/pages/SupportPage.svelte'

const account = { id: 'account-test', email: 'tester@example.invalid', emailVerified: true, betaAccess: true }

function summary(overrides: Partial<SupportTicketSummary> = {}): SupportTicketSummary {
  return { id: 'ticket-sync', subject: 'Sync keeps failing', status: 'open', category: 'sync', appVersion: '', diagnosticCode: '', browserIntegration: '', requestId: '', messageCount: 1, unreadCount: 0, createdAt: '2026-10-01T10:00:00Z', updatedAt: '2026-10-20T10:00:00Z', autoClosed: false, canClose: true, canReopen: false, ...overrides }
}

beforeEach(() => {
  sessionStorage.clear()
  support.getSupportMetadata.mockResolvedValue({ status: 'open', url: '', intake: '', attachmentsAccepted: false, receiptEmail: true })
  support.getSupportTickets.mockResolvedValue([])
  auth.getNotificationPreferences.mockResolvedValue({ supportReplies: true })
  window.history.replaceState({}, '', '/support')
})

afterEach(() => {
  cleanup()
  sessionStorage.clear()
  Object.values(support).forEach((mock) => mock.mockReset())
})

async function type(label: string, value: string) {
  await fireEvent.input(screen.getByLabelText(label), { target: { value } })
}

test('a title that matches a known answer shows it before the request is sent, with its link', async () => {
  render(SupportPage, { account: null })
  await type('Short title', 'I forgot password again')
  const answer = await screen.findByText('Reset your account password')
  expect(answer).toBeTruthy()
  expect(screen.getByText('Use the recovery page to choose a new one.')).toBeTruthy()
  expect(screen.getByRole('link', { name: 'Recover your account' }).getAttribute('href')).toBe('/forgot-password')
})

test('nothing is suggested for text that matches no answer', async () => {
  render(SupportPage, { account: null })
  await type('Short title', 'The icon looks blurry on my screen')
  expect(screen.queryByText('Reset your account password')).toBeNull()
})

test('an answer that is not what the person needs can be dismissed and stays dismissed while they type', async () => {
  render(SupportPage, { account: null })
  await type('Short title', 'I forgot password again')
  await fireEvent.click(await screen.findByRole('button', { name: 'Not what I need' }))
  expect(screen.queryByText('Reset your account password')).toBeNull()
  await type('Short title', 'I forgot password again and again')
  expect(screen.queryByText('Reset your account password')).toBeNull()
})

test('the chosen topic changes what to include and what the message box asks', async () => {
  render(SupportPage, { account: null })
  await fireEvent.change(screen.getByLabelText('Topic'), { target: { value: 'sync' } })
  expect(await screen.findByText('What the Sync panel says on each device')).toBeTruthy()
  expect((screen.getByLabelText('Message') as HTMLTextAreaElement).placeholder).toBe('Which devices?')
  expect(screen.getAllByText('Never send').length).toBeGreaterThan(0)
})

test('an open request on the same topic is pointed out and opens in one click', async () => {
  support.getSupportTickets.mockResolvedValue([summary()])
  support.getSupportTicket.mockResolvedValue({ ...summary(), messages: [] } as SupportTicketDetail)
  render(SupportPage, { account })
  await fireEvent.change(await screen.findByLabelText('Topic'), { target: { value: 'sync' } })
  expect((await screen.findByRole('status', { name: 'Existing request' })).textContent).toContain('Sync keeps failing')
  support.getSupportTicket.mockClear()
  await fireEvent.click(screen.getByRole('button', { name: 'Open request' }))
  expect(support.getSupportTicket).toHaveBeenCalledWith('ticket-sync')
})

test('a closed request on the same topic is not pointed out', async () => {
  support.getSupportTickets.mockResolvedValue([summary({ status: 'closed' })])
  render(SupportPage, { account })
  await fireEvent.change(await screen.findByLabelText('Topic'), { target: { value: 'sync' } })
  expect(screen.queryByRole('status', { name: 'Existing request' })).toBeNull()
})

test('a half written request survives a reload and is cleared once sent', async () => {
  const first = render(SupportPage, { account: null })
  await type('Short title', 'Devices disagree')
  await type('Message', 'Two devices show different logins after I changed the vault key.')
  await fireEvent.change(screen.getByLabelText('Topic'), { target: { value: 'sync' } })
  first.unmount()
  render(SupportPage, { account: null })
  expect((screen.getByLabelText('Short title') as HTMLInputElement).value).toBe('Devices disagree')
  expect((screen.getByLabelText('Message') as HTMLTextAreaElement).value).toContain('different logins')
  expect((screen.getByLabelText('Topic') as HTMLSelectElement).value).toBe('sync')
  support.submitSupportRequest.mockResolvedValue({ requestId: 'req-1', status: 'open' })
  await type('Email', 'guest@example.invalid')
  await fireEvent.click(screen.getByRole('button', { name: 'Send request' }))
  await screen.findByText(/Request req-1/)
  expect(sessionStorage.getItem('sesame-support-draft-v1')).toBeNull()
})

test('a link in the address does not get overwritten by an old draft', async () => {
  sessionStorage.setItem('sesame-support-draft-v1', JSON.stringify({ category: 'bug', subject: 'Old draft', message: 'Old text' }))
  window.history.replaceState({}, '', '/support?intent=sync')
  render(SupportPage, { account: null })
  expect((screen.getByLabelText('Short title') as HTMLInputElement).value).toBe('Sesame Sync interest')
})

test('a Sesame recovery kit typed into the message is refused on the spot and never saved', async () => {
  render(SupportPage, { account: null })
  await type('Message', 'My kit is F9K4P-7XQ2M-T6V8C-H3R5W-J8L2N please help me sign in to my vault today')
  expect(screen.getByText(/This looks like a Sesame recovery kit/)).toBeTruthy()
  expect((screen.getByRole('button', { name: 'Send request' }) as HTMLButtonElement).disabled).toBe(true)
  expect(sessionStorage.getItem('sesame-support-draft-v1')).toBeNull()
})
