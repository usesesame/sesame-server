import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/svelte'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'

const auth = vi.hoisted(() => ({
  loadAuthState: vi.fn(),
  getAccountBootstrap: vi.fn(),
  getNotificationPreferences: vi.fn(),
  listSessions: vi.fn(),
  listDesktopDevices: vi.fn(),
  getDesktopLink: vi.fn(),
  getAccountActivity: vi.fn(),
  signOut: vi.fn(),
}))

const capabilities = vi.hoisted(() => ({
  capabilities: vi.fn(),
  capabilityEnabled: (config: { features: Record<string, boolean> }, feature: string) => config.features[feature] === true,
}))

vi.mock('../src/lib/auth', async (importOriginal) => ({ ...(await importOriginal<typeof import('../src/lib/auth')>()), ...auth }))
vi.mock('../src/lib/capabilities', () => capabilities)
vi.mock('../src/lib/passkey', () => ({ passkeysSupported: () => false, listPasskeys: vi.fn(), deletePasskey: vi.fn(), registerPasskey: vi.fn() }))
vi.mock('../src/lib/runtime-config', () => ({ siteOrigin: 'https://website.test.invalid', apiBaseURL: 'https://api.test.invalid' }))

import App from '../src/App.svelte'

const account = { id: 'account-test', email: 'tester@example.invalid', emailVerified: true, betaAccess: true }

beforeEach(() => {
  auth.loadAuthState.mockResolvedValue({ state: 'authenticated', account })
  auth.getAccountBootstrap.mockResolvedValue({
    account,
    deploymentProfile: 'project',
    access: { betaAccess: true, emailVerified: true, downloadsAllowed: true, licences: [] },
    licences: [],
    capabilities: { desktopLinking: true, passkeys: true, browserHelper: false, notifications: true },
    notificationCounts: { security: 0, support: 2, product: 0 },
    security: { activeSessions: 1, connectedDesktops: 1, recentAuthenticationAt: '2026-11-01T10:00:00Z', credentialSetupRequired: false },
  })
  auth.getNotificationPreferences.mockResolvedValue({ betaReleases: false, supportReplies: true, productAnnouncements: false })
  auth.listSessions.mockResolvedValue([])
  auth.listDesktopDevices.mockResolvedValue([])
  auth.getDesktopLink.mockResolvedValue({ state: 'none' })
  auth.getAccountActivity.mockResolvedValue([])
  capabilities.capabilities.mockResolvedValue({ schemaVersion: 1, minimumDesktopVersion: '0.1.0', latestDesktopVersion: '0.1.0', features: { desktopLinking: true }, serviceStatus: {}, expiresAt: '2099-01-01T00:00:00Z' })
})

afterEach(() => {
  cleanup()
  Object.values(auth).forEach((mock) => mock.mockReset())
  capabilities.capabilities.mockReset()
  vi.unstubAllGlobals()
  window.history.replaceState({}, '', '/')
})

function openAt(path: string) {
  window.history.replaceState({}, '', path)
  return render(App, { initialPath: path.split('#')[0] })
}

function sidebar() {
  return within(screen.getByRole('navigation', { name: 'Portal navigation' }))
}

test('the sidebar lists every account page and Support, with the unread count on Support', async () => {
  openAt('/account/security')
  const links = sidebar().getAllByRole('link')
  expect(links.map((link) => [link.textContent?.trim(), link.getAttribute('href')])).toEqual([
    ['Overview', '/account'],
    ['Security', '/account/security'],
    ['Sessions', '/account/sessions'],
    ['Desktops', '/account/desktops'],
    ['Activity', '/account/activity'],
    [expect.stringMatching(/^Support/), '/support'],
  ])
  expect(await sidebar().findByLabelText('2 unread support replies')).toBeTruthy()
  expect(await screen.findByText('tester@example.invalid', { selector: '.sidebar-email' })).toBeTruthy()
  expect(screen.getByRole('link', { name: 'website.test.invalid' }).getAttribute('href')).toBe('https://website.test.invalid')
})

test.each([
  ['/', 'Overview'],
  ['/account', 'Overview'],
  ['/account/security', 'Security'],
  ['/account/sessions', 'Sessions'],
  ['/account/desktops', 'Desktops'],
  ['/account/activity', 'Activity'],
  ['/account/activity/', 'Activity'],
])('%s opens the %s page and marks it in the sidebar', async (path, name) => {
  openAt(path)
  expect(await screen.findByRole('heading', { level: 1, name })).toBeTruthy()
  expect(screen.getAllByRole('heading', { level: 1 })).toHaveLength(1)
  const current = sidebar().getAllByRole('link').filter((link) => link.getAttribute('aria-current') === 'page')
  expect(current.map((link) => link.textContent?.trim())).toEqual([name])
})

test('the old security link with a hash opens Security and moves to its own address', async () => {
  openAt('/account#security')
  expect(await screen.findByRole('heading', { level: 1, name: 'Security' })).toBeTruthy()
  expect(window.location.pathname).toBe('/account/security')
  expect(window.location.hash).toBe('')
})

test('choosing a page in the sidebar changes the address, focuses the title, and the back button returns', async () => {
  openAt('/account')
  await screen.findByRole('heading', { level: 1, name: 'Overview' })
  await fireEvent.click(sidebar().getByRole('link', { name: 'Sessions' }))
  const title = await screen.findByRole('heading', { level: 1, name: 'Sessions' })
  expect(window.location.pathname).toBe('/account/sessions')
  await waitFor(() => expect(document.activeElement).toBe(title))
  expect(sidebar().getByRole('link', { name: 'Sessions' }).getAttribute('aria-current')).toBe('page')
  expect(auth.listSessions).toHaveBeenCalledOnce()

  window.history.back()
  await waitFor(() => expect(window.location.pathname).toBe('/account'))
  expect(await screen.findByRole('heading', { level: 1, name: 'Overview' })).toBeTruthy()
  expect(sidebar().getByRole('link', { name: 'Overview' }).getAttribute('aria-current')).toBe('page')
})

test('the Support link leaves the account pages with an ordinary navigation', async () => {
  openAt('/account')
  await screen.findByRole('heading', { level: 1, name: 'Overview' })
  const link = sidebar().getByRole('link', { name: /Support/ })
  const event = new MouseEvent('click', { bubbles: true, cancelable: true, button: 0 })
  link.addEventListener('click', (clicked) => clicked.preventDefault(), { once: true })
  const notCancelledByApp = link.dispatchEvent(event)
  expect(notCancelledByApp).toBe(false)
  expect(window.location.pathname).toBe('/account')
})

test('the phone menu opens, closes on Escape and returns focus to the Menu button', async () => {
  openAt('/account')
  const menu = await screen.findByRole('button', { name: 'Menu' })
  expect(menu.getAttribute('aria-expanded')).toBe('false')
  expect(menu.getAttribute('aria-controls')).toBe('portal-menu')
  expect(document.getElementById('portal-menu')?.contains(screen.getByRole('navigation', { name: 'Portal navigation' }))).toBe(true)
  await fireEvent.click(menu)
  expect(menu.getAttribute('aria-expanded')).toBe('true')
  expect(document.querySelector('.sidebar')?.classList.contains('open')).toBe(true)
  expect(screen.getByRole('button', { name: 'Close the menu' })).toBeTruthy()
  sidebar().getByRole('link', { name: 'Security' }).focus()
  await fireEvent.keyDown(document.activeElement as Element, { key: 'Escape' })
  expect(menu.getAttribute('aria-expanded')).toBe('false')
  await waitFor(() => expect(document.activeElement).toBe(menu))
  expect(screen.queryByRole('button', { name: 'Close the menu' })).toBeNull()
})

test('choosing a link in the phone menu closes it, opens the page and returns focus to the Menu button', async () => {
  openAt('/account')
  const menu = await screen.findByRole('button', { name: 'Menu' })
  await fireEvent.click(menu)
  await fireEvent.click(sidebar().getByRole('link', { name: 'Desktops' }))
  expect(await screen.findByRole('heading', { level: 1, name: 'Desktops' })).toBeTruthy()
  expect(window.location.pathname).toBe('/account/desktops')
  expect(menu.getAttribute('aria-expanded')).toBe('false')
  await waitFor(() => expect(document.activeElement).toBe(menu))
})

test('the scrim closes the phone menu', async () => {
  openAt('/account')
  const menu = await screen.findByRole('button', { name: 'Menu' })
  await fireEvent.click(menu)
  await fireEvent.click(screen.getByRole('button', { name: 'Close the menu' }))
  expect(menu.getAttribute('aria-expanded')).toBe('false')
  await waitFor(() => expect(document.activeElement).toBe(menu))
})

test('Sign out in the sidebar signs out and goes to the start page', async () => {
  const assign = vi.fn()
  openAt('/account')
  vi.stubGlobal('location', { assign, replace: vi.fn(), search: '', pathname: '/account', hash: '', origin: 'http://localhost', href: 'http://localhost/account' })
  auth.signOut.mockResolvedValue(undefined)
  await screen.findByRole('heading', { level: 1, name: 'Overview' })
  await fireEvent.click(screen.getByRole('button', { name: 'Sign out' }))
  await waitFor(() => expect(assign).toHaveBeenCalledWith('/'))
  expect(auth.signOut).toHaveBeenCalledOnce()
})

test('a failed sign out says so beside the button', async () => {
  openAt('/account')
  auth.signOut.mockRejectedValue(new Error('Sign out is temporarily unavailable.'))
  await screen.findByRole('heading', { level: 1, name: 'Overview' })
  await fireEvent.click(screen.getByRole('button', { name: 'Sign out' }))
  expect((await screen.findByRole('alert')).textContent).toBe('Sign out is temporarily unavailable.')
})

test('the signed-in shell has a skip link to the main content and no marketing footer', async () => {
  openAt('/account')
  await screen.findByRole('heading', { level: 1, name: 'Overview' })
  expect(screen.getByRole('link', { name: 'Skip to content' }).getAttribute('href')).toBe('#top')
  expect(screen.getByRole('main').id).toBe('top')
  expect(screen.queryByRole('contentinfo')).toBeNull()
  expect(screen.queryByRole('tab')).toBeNull()
})
