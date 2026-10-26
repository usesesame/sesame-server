import { cleanup, fireEvent, render, screen } from '@testing-library/svelte'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import type { Account, AccountBootstrap } from '../src/lib/auth'

const auth = vi.hoisted(() => ({
  cancelDesktopLink: vi.fn(),
  changePassword: vi.fn(),
  createDesktopLink: vi.fn(),
  deleteAccount: vi.fn(),
  getAccountBootstrap: vi.fn(),
  getAccountActivity: vi.fn(),
  getNotificationPreferences: vi.fn(),
  updateNotificationPreferences: vi.fn(),
  getDesktopLink: vi.fn(),
  listDesktopDevices: vi.fn(),
  listSessions: vi.fn(),
  reauthenticate: vi.fn(),
  requestEmailChange: vi.fn(),
  requestEmailVerification: vi.fn(),
  revokeAllSessions: vi.fn(),
  revokeDesktopDevice: vi.fn(),
  renameDesktopDevice: vi.fn(),
  revokeSession: vi.fn(),
  setupPassword: vi.fn(),
  signOut: vi.fn(),
}))

const passkey = vi.hoisted(() => ({
  deletePasskey: vi.fn(),
  listPasskeys: vi.fn(),
  passkeysSupported: vi.fn(() => false),
  registerPasskey: vi.fn(),
}))

const capabilities = vi.hoisted(() => ({
  capabilities: vi.fn(),
  capabilityEnabled: (config: { features: Record<string, boolean> }, feature: string) => config.features[feature] === true,
}))

vi.mock('../src/lib/auth', () => auth)
vi.mock('../src/lib/passkey', () => passkey)
vi.mock('../src/lib/capabilities', () => capabilities)
vi.mock('../src/lib/runtime-config', () => ({ siteOrigin: 'https://website.test.invalid' }))

import AccountPage from '../src/pages/AccountPage.svelte'

const account: Account = { id: 'account-test', email: 'tester@example.invalid', emailVerified: true, betaAccess: true }
const authenticated = { state: 'authenticated', account } as const

function bootstrap(overrides: Partial<AccountBootstrap> = {}): AccountBootstrap {
  return {
    account,
    deploymentProfile: 'project',
    access: { betaAccess: true, emailVerified: true, downloadsAllowed: true, licences: [] },
    licences: [],
    capabilities: { desktopLinking: true, passkeys: true, browserHelper: false, notifications: true },
    notificationCounts: { security: 0, support: 0, product: 0 },
    security: { activeSessions: 1, connectedDesktops: 0, recentAuthenticationAt: '2026-08-31T10:00:00Z', credentialSetupRequired: false },
    ...overrides,
  }
}

function mockAccess(data = bootstrap()) {
  auth.getAccountBootstrap.mockResolvedValue(data)
  capabilities.capabilities.mockResolvedValue({
    schemaVersion: 1,
    minimumDesktopVersion: '0.1.0',
    latestDesktopVersion: '0.1.0',
    features: { desktopLinking: true, downloads: false },
    serviceStatus: {},
    expiresAt: '2099-01-01T00:00:00Z',
  })
}

beforeEach(() => {
  vi.stubGlobal('ResizeObserver', class {
    observe() {}
    disconnect() {}
  })
})

afterEach(() => {
  cleanup()
  Object.values(auth).forEach((mock) => mock.mockReset())
  Object.values(passkey).forEach((mock) => mock.mockReset())
  passkey.passkeysSupported.mockReturnValue(false)
  capabilities.capabilities.mockReset()
  vi.unstubAllGlobals()
  window.history.replaceState({}, '', '/account')
})

test('loads and saves the optional notification preferences', async () => {
  mockAccess()
  auth.getNotificationPreferences.mockResolvedValue({ betaReleases: true, supportReplies: false, productAnnouncements: false })
  auth.updateNotificationPreferences.mockResolvedValue(undefined)
  render(AccountPage, { account, authState: authenticated, onSignedOut: vi.fn() })
  await fireEvent.click(screen.getByRole('tab', { name: 'Security' }))
  const betaReleases = await screen.findByRole('checkbox', { name: 'New beta releases' }) as HTMLInputElement
  expect(betaReleases.checked).toBe(true)
  expect((screen.getByRole('checkbox', { name: 'Support replies' }) as HTMLInputElement).checked).toBe(false)
  expect((screen.getByRole('checkbox', { name: 'Product announcements' }) as HTMLInputElement).checked).toBe(false)
  await fireEvent.click(screen.getByRole('checkbox', { name: 'Support replies' }))
  await fireEvent.click(screen.getByRole('button', { name: 'Save preferences' }))
  expect(await screen.findByText('Notification preferences updated.')).toBeTruthy()
  expect(auth.updateNotificationPreferences).toHaveBeenCalledWith({ betaReleases: true, supportReplies: true, productAnnouncements: false })
})

test('shows a preference loading failure', async () => {
  mockAccess()
  auth.getNotificationPreferences.mockRejectedValue(new Error('The account service is temporarily unavailable.'))
  render(AccountPage, { account, authState: authenticated, onSignedOut: vi.fn() })
  await fireEvent.click(screen.getByRole('tab', { name: 'Security' }))
  expect((await screen.findByRole('alert')).textContent).toContain('The account service is temporarily unavailable.')
})

test('opens the security tab from the support preference link', async () => {
  mockAccess()
  auth.getNotificationPreferences.mockResolvedValue({ betaReleases: true, supportReplies: true, productAnnouncements: false })
  window.history.replaceState({}, '', '/account#security')
  render(AccountPage, { account, authState: authenticated, onSignedOut: vi.fn() })
  expect(await screen.findByRole('checkbox', { name: 'Support replies' })).toBeTruthy()
})

test('has no downloads tab because every release is public', async () => {
  mockAccess()
  render(AccountPage, { account, authState: authenticated, onSignedOut: vi.fn() })
  await screen.findByText('Beta access')
  expect(screen.queryByRole('tab', { name: 'Downloads' })).toBeNull()
})

test('summarises access and signs out from the overview', async () => {
  const assign = vi.fn()
  vi.stubGlobal('location', { assign, search: '', pathname: '/', hash: '', origin: 'http://localhost', href: 'http://localhost/' })
  mockAccess(bootstrap({
    access: { betaAccess: true, emailVerified: true, downloadsAllowed: true, licences: [{ id: 'licence-test', product: 'Sesame Pro', status: 'active', issuedAt: '2026-08-01T00:00:00Z' }] },
    notificationCounts: { security: 0, support: 3, product: 0 },
  }))
  auth.signOut.mockResolvedValue(undefined)
  const onSignedOut = vi.fn()
  render(AccountPage, { account, authState: authenticated, onSignedOut })
  expect(await screen.findByText('3 unread')).toBeTruthy()
  expect(screen.getByText('Granted')).toBeTruthy()
  await fireEvent.click(screen.getByRole('button', { name: 'Sign out' }))
  expect(auth.signOut).toHaveBeenCalledOnce()
  expect(onSignedOut).toHaveBeenCalledOnce()
  expect(assign).toHaveBeenCalledWith('/')
})

test('shows the signed-out state without an account', () => {
  render(AccountPage, { account: null, authState: { state: 'anonymous' }, onSignedOut: vi.fn() })
  expect(screen.getByText('You are signed out')).toBeTruthy()
  expect(screen.getByRole('link', { name: 'Sign in' })).toBeTruthy()
})

test('asks an account without credentials to set a password and then hides the prompt', async () => {
  mockAccess(bootstrap({ security: { activeSessions: 1, connectedDesktops: 0, recentAuthenticationAt: '2026-08-31T10:00:00Z', credentialSetupRequired: true } }))
  auth.setupPassword.mockResolvedValue(undefined)
  render(AccountPage, { account, authState: authenticated, onSignedOut: vi.fn() })

  expect(await screen.findByText('Set a password')).toBeTruthy()
  await fireEvent.input(screen.getAllByLabelText(/new password/i)[0], { target: { value: 'fictional-first-password' } })
  await fireEvent.input(screen.getByLabelText('Confirm new password'), { target: { value: 'fictional-first-password' } })
  await fireEvent.click(screen.getByRole('button', { name: 'Set password' }))

  expect(auth.setupPassword).toHaveBeenCalledWith('fictional-first-password')
  expect(await screen.findByText('Password set.')).toBeTruthy()
  expect(screen.queryByText('Set a password')).toBeNull()
})

test('does not ask for a password when the account already has credentials', async () => {
  mockAccess()
  render(AccountPage, { account, authState: authenticated, onSignedOut: vi.fn() })
  expect(await screen.findByText('Signed in as')).toBeTruthy()
  expect(screen.queryByText('Set a password')).toBeNull()
})

test('a new account is told to verify its email first and can send the link again', async () => {
  const unverified: Account = { ...account, emailVerified: false }
  mockAccess(bootstrap({ account: unverified, access: { betaAccess: true, emailVerified: false, downloadsAllowed: true, licences: [] } }))
  auth.requestEmailVerification.mockResolvedValue(undefined)
  render(AccountPage, { account: unverified, authState: { state: 'authenticated', account: unverified }, onSignedOut: vi.fn() })
  expect(await screen.findByRole('heading', { name: 'Verify your email' })).toBeTruthy()
  expect(screen.getByText(/tester@example.invalid/, { selector: 'p' })).toBeTruthy()
  await fireEvent.click(screen.getByRole('button', { name: 'Send verification email' }))
  expect(auth.requestEmailVerification).toHaveBeenCalledOnce()
})

test('a verified account with no desktop is pointed at connecting one, and the button opens that tab', async () => {
  mockAccess()
  auth.listDesktopDevices.mockResolvedValue([])
  auth.getDesktopLink.mockResolvedValue(null)
  render(AccountPage, { account, authState: authenticated, onSignedOut: vi.fn() })
  expect(await screen.findByRole('heading', { name: 'Connect Sesame on your computer' })).toBeTruthy()
  expect(screen.getByRole('link', { name: 'Download Sesame' }).getAttribute('href')).toBe('https://website.test.invalid/releases')
  expect(screen.getByText('None connected')).toBeTruthy()
  await fireEvent.click(screen.getByRole('button', { name: 'Create a link code' }))
  expect(screen.getByRole('tab', { name: 'Desktops' }).getAttribute('aria-selected')).toBe('true')
})

test('an account that already has a desktop shows no next step and counts it', async () => {
  mockAccess(bootstrap({ security: { activeSessions: 2, connectedDesktops: 2, recentAuthenticationAt: '2026-08-31T10:00:00Z', credentialSetupRequired: false } }))
  render(AccountPage, { account, authState: authenticated, onSignedOut: vi.fn() })
  expect(await screen.findByText('2 connected')).toBeTruthy()
  expect(screen.queryByRole('heading', { name: 'Connect Sesame on your computer' })).toBeNull()
  expect(screen.queryByRole('heading', { name: 'Verify your email' })).toBeNull()
})

test('an account without beta access is told how to ask, not shown the connect step', async () => {
  mockAccess(bootstrap({ access: { betaAccess: false, emailVerified: true, downloadsAllowed: true, licences: [] } }))
  render(AccountPage, { account: { ...account, betaAccess: false }, authState: { state: 'authenticated', account: { ...account, betaAccess: false } }, onSignedOut: vi.fn() })
  expect(await screen.findByRole('heading', { name: 'Ask for beta access' })).toBeTruthy()
  expect(screen.getByRole('link', { name: 'Ask for access' }).getAttribute('href')).toBe('/support/request')
  expect(screen.getByText('Not granted')).toBeTruthy()
})

test('no step is shown before the account details have loaded', () => {
  auth.getAccountBootstrap.mockReturnValue(new Promise(() => {}))
  capabilities.capabilities.mockReturnValue(new Promise(() => {}))
  render(AccountPage, { account, authState: authenticated, onSignedOut: vi.fn() })
  expect(screen.queryByRole('heading', { name: 'Connect Sesame on your computer' })).toBeNull()
  expect(screen.queryByRole('heading', { name: 'Verify your email' })).toBeNull()
})

test('choosing a tab brings it into view when the tab row scrolls on a phone', async () => {
  mockAccess()
  auth.listDesktopDevices.mockResolvedValue([])
  auth.getDesktopLink.mockResolvedValue(null)
  const scrolled = vi.fn()
  Element.prototype.scrollIntoView = scrolled
  render(AccountPage, { account, authState: authenticated, onSignedOut: vi.fn() })
  await screen.findByText('Beta access')
  await fireEvent.click(screen.getByRole('tab', { name: 'Desktops' }))
  await vi.waitFor(() => expect(scrolled).toHaveBeenCalledWith({ inline: 'nearest', block: 'nearest' }))
  expect(scrolled.mock.contexts.at(-1)).toBe(screen.getByRole('tab', { name: 'Desktops' }))
})
