import { cleanup, fireEvent, render, screen } from '@testing-library/svelte'
import { afterEach, expect, test, vi } from 'vitest'
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
  getAccountDownloads: vi.fn(),
  createDownloadTicket: vi.fn(),
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
    access: { betaAccess: true, emailVerified: true, downloadsAllowed: true, licences: [] },
    licences: [],
    capabilities: { desktopLinking: true, passkeys: true, browserHelper: false, notifications: true },
    notificationCounts: { security: 0, support: 0, product: 0 },
    security: { activeSessions: 1, connectedDesktops: 0, recentAuthenticationAt: '2026-08-31T10:00:00Z' },
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
  auth.getAccountDownloads.mockResolvedValue([])
}

afterEach(() => {
  cleanup()
  Object.values(auth).forEach((mock) => mock.mockReset())
  Object.values(passkey).forEach((mock) => mock.mockReset())
  passkey.passkeysSupported.mockReturnValue(false)
  capabilities.capabilities.mockReset()
  vi.unstubAllGlobals()
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
  expect(screen.getByText('Eligible')).toBeTruthy()
  expect(screen.getByText('Available')).toBeTruthy()
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
