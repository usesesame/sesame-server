import { cleanup, waitFor, render, screen, within } from '@testing-library/svelte'
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
  const navigation = within(await screen.findByRole('navigation', { name: 'Portal navigation' }))
  expect(await navigation.findByLabelText('3 unread support replies')).toBeTruthy()
})

test('hides the support count when it is zero', async () => {
  auth.loadAuthState.mockResolvedValue({ state: 'authenticated', account })
  auth.getAccountBootstrap.mockResolvedValue({ notificationCounts: { security: 0, support: 0, product: 0 } })
  auth.getNotificationPreferences.mockResolvedValue({ betaReleases: true, supportReplies: true, productAnnouncements: false })
  support.getSupportTickets.mockResolvedValue([])
  support.getSupportMetadata.mockResolvedValue({ receiptEmail: true })
  render(App, { initialPath: '/support' })
  expect(await screen.findByRole('heading', { level: 2, name: 'Your requests' })).toBeTruthy()
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

test('sends a signed-out visitor from the account address to the sign-in page', async () => {
  const replace = vi.fn()
  vi.stubGlobal('location', { replace, assign: vi.fn(), search: '', pathname: '/account', hash: '', origin: 'http://localhost', href: 'http://localhost/account' })
  auth.loadAuthState.mockResolvedValue({ state: 'anonymous', expired: false })
  render(App, { initialPath: '/account' })
  await waitFor(() => expect(replace).toHaveBeenCalledWith('/login'))
  expect(screen.queryByText('You are signed out')).toBeNull()
  vi.unstubAllGlobals()
})

test('keeps the expired session notice when it sends the visitor to sign in', async () => {
  const replace = vi.fn()
  vi.stubGlobal('location', { replace, assign: vi.fn(), search: '', pathname: '/account', hash: '', origin: 'http://localhost', href: 'http://localhost/account' })
  auth.loadAuthState.mockResolvedValue({ state: 'anonymous', expired: true })
  render(App, { initialPath: '/account' })
  await waitFor(() => expect(replace).toHaveBeenCalledWith('/login?expired=1'))
  vi.unstubAllGlobals()
})

test('sends a visitor to sign in when the account service is unavailable and shows the notice there', async () => {
  const replace = vi.fn()
  vi.stubGlobal('location', { replace, assign: vi.fn(), search: '', pathname: '/account', hash: '', origin: 'http://localhost', href: 'http://localhost/account' })
  auth.loadAuthState.mockResolvedValue({ state: 'offline' })
  render(App, { initialPath: '/account' })
  await waitFor(() => expect(replace).toHaveBeenCalledWith('/login'))
  cleanup()
  vi.unstubAllGlobals()

  auth.loadAuthState.mockResolvedValue({ state: 'offline' })
  render(App, { initialPath: '/login' })
  expect((await screen.findByRole('alert')).textContent).toContain('Reconnect and try again.')
  expect(screen.getByRole('button', { name: 'Try again' })).toBeTruthy()
})

test.each(['/login', '/register', '/forgot-password'])('%s shows a slim bar with the brand and the website link, without the portal navigation or a Sign in button', async (path) => {
  auth.loadAuthState.mockResolvedValue({ state: 'anonymous', expired: false })
  render(App, { initialPath: path })
  await screen.findByRole('heading', { level: 1 })
  expect(screen.queryByRole('navigation', { name: 'Portal navigation' })).toBeNull()
  const header = within(screen.getByRole('banner'))
  expect(header.getByRole('link', { name: /Sesame/ }).getAttribute('href')).toBe('/account')
  expect(header.getByText('Account')).toBeTruthy()
  expect(header.getByRole('link', { name: 'website.test.invalid' }).getAttribute('href')).toBe('https://website.test.invalid')
  expect(header.queryByRole('link', { name: 'Sign in' })).toBeNull()
  expect(within(screen.getByRole('contentinfo')).getByRole('link', { name: 'Support' }).getAttribute('href')).toBe('/support')
})

test.each(['/support', '/terms', '/nowhere'])('%s offers one Sign in button in the slim bar to a signed-out visitor', async (path) => {
  auth.loadAuthState.mockResolvedValue({ state: 'anonymous', expired: false })
  support.getSupportMetadata.mockResolvedValue({ receiptEmail: true })
  render(App, { initialPath: path })
  await screen.findByRole('heading', { level: 1 })
  const header = within(screen.getByRole('banner'))
  expect((await header.findAllByRole('link', { name: 'Sign in' }))).toHaveLength(1)
  expect(screen.queryByRole('navigation', { name: 'Portal navigation' })).toBeNull()
  expect(screen.getAllByRole('heading', { level: 1 })).toHaveLength(1)
})

test.each(['/login', '/register'])('%s has a short footer with the legal links and no large link columns', async (path) => {
  auth.loadAuthState.mockResolvedValue({ state: 'anonymous', expired: false })
  render(App, { initialPath: path })
  await screen.findByRole('heading', { level: 1 })
  const footer = within(screen.getByRole('contentinfo'))
  expect(footer.getByRole('link', { name: 'Privacy' }).getAttribute('href')).toBe('https://website.test.invalid/privacy')
  expect(footer.getByRole('link', { name: 'Terms' }).getAttribute('href')).toBe('https://website.test.invalid/terms')
  expect(footer.queryByRole('navigation', { name: 'Portal' })).toBeNull()
})

test('a signed-in visitor on Support gets the sidebar with Support marked and no marketing footer', async () => {
  auth.loadAuthState.mockResolvedValue({ state: 'authenticated', account })
  auth.getAccountBootstrap.mockResolvedValue({ notificationCounts: { security: 0, support: 0, product: 0 } })
  auth.getNotificationPreferences.mockResolvedValue({ betaReleases: true, supportReplies: true, productAnnouncements: false })
  support.getSupportTickets.mockResolvedValue([])
  support.getSupportMetadata.mockResolvedValue({ receiptEmail: true })
  render(App, { initialPath: '/support' })
  const navigation = within(await screen.findByRole('navigation', { name: 'Portal navigation' }))
  expect(navigation.getByRole('link', { name: 'Support' }).getAttribute('aria-current')).toBe('page')
  expect(screen.queryByRole('contentinfo')).toBeNull()
  expect(screen.queryByRole('banner')).toBeNull()
  expect(screen.getAllByRole('heading', { level: 1 }).map((heading) => heading.textContent)).toEqual(['Support'])
})

test('loading the account page shows one spinner, in the page and not in the header', async () => {
  auth.loadAuthState.mockReturnValue(new Promise(() => {}))
  render(App, { initialPath: '/account' })
  expect(screen.getByText('Loading your account')).toBeTruthy()
  expect(document.querySelectorAll('.session-spinner')).toHaveLength(1)
  expect(screen.queryByLabelText('Checking account session')).toBeNull()
  expect(screen.getByRole('navigation', { name: 'Portal navigation' })).toBeTruthy()
})
