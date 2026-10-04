import { cleanup, render, screen, within } from '@testing-library/svelte'
import { afterEach, expect, test, vi } from 'vitest'

const auth = vi.hoisted(() => ({
  loadAuthState: vi.fn(),
  getAccountBootstrap: vi.fn(),
  getNotificationPreferences: vi.fn(),
}))

const support = vi.hoisted(() => ({
  getSupportTickets: vi.fn(),
  getSupportMetadata: vi.fn(),
}))

vi.mock('../src/lib/auth', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../src/lib/auth')>()
  return { ...actual, ...auth }
})
vi.mock('../src/lib/support', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../src/lib/support')>()
  return { ...actual, ...support }
})
vi.mock('../src/lib/runtime-config', () => ({ siteOrigin: 'https://website.test.invalid', apiBaseURL: 'https://api.test.invalid' }))

import App from '../src/App.svelte'

const account = { id: 'account-test', email: 'tester@example.invalid', emailVerified: true, betaAccess: true }

afterEach(() => {
  cleanup()
  Object.values(auth).forEach((mock) => mock.mockReset())
  Object.values(support).forEach((mock) => mock.mockReset())
  window.history.replaceState({}, '', '/')
})

test('shows the unread support count on the Support links', async () => {
  auth.loadAuthState.mockResolvedValue({ state: 'authenticated', account })
  auth.getAccountBootstrap.mockResolvedValue({ notificationCounts: { security: 0, support: 3, product: 0 } })
  auth.getNotificationPreferences.mockResolvedValue({ betaReleases: true, supportReplies: true, productAnnouncements: false })
  support.getSupportTickets.mockResolvedValue([])
  support.getSupportMetadata.mockResolvedValue({ receiptEmail: true })
  render(App, { initialPath: '/support' })
  const navigation = within(screen.getByRole('navigation', { name: 'Portal navigation' }))
  expect(await navigation.findByLabelText('3 unread support replies')).toBeTruthy()
})

test('hides the support count when it is zero', async () => {
  auth.loadAuthState.mockResolvedValue({ state: 'authenticated', account })
  auth.getAccountBootstrap.mockResolvedValue({ notificationCounts: { security: 0, support: 0, product: 0 } })
  auth.getNotificationPreferences.mockResolvedValue({ betaReleases: true, supportReplies: true, productAnnouncements: false })
  support.getSupportTickets.mockResolvedValue([])
  support.getSupportMetadata.mockResolvedValue({ receiptEmail: true })
  render(App, { initialPath: '/support' })
  expect(await screen.findByText('Signed in as tester@example.invalid')).toBeTruthy()
  expect(screen.queryByLabelText(/unread support/)).toBeNull()
})

test('shows an expired session message once on the sign-in page', async () => {
  auth.loadAuthState.mockResolvedValue({ state: 'anonymous', expired: true })
  render(App, { initialPath: '/login' })
  expect(await screen.findByText('Your session expired. Sign in again to continue.')).toBeTruthy()
})

test('does not show the expired session message for an ordinary anonymous visit', async () => {
  auth.loadAuthState.mockResolvedValue({ state: 'anonymous', expired: false })
  render(App, { initialPath: '/login' })
  expect(await screen.findByRole('heading', { name: 'Sign in' })).toBeTruthy()
  expect(screen.queryByText('Your session expired. Sign in again to continue.')).toBeNull()
})
