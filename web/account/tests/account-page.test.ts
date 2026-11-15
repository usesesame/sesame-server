import { cleanup, fireEvent, render, screen, within } from '@testing-library/svelte'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import type { Account, AccountBootstrap, AccountSession, DesktopDevice } from '../src/lib/auth'

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
  render(AccountPage, { account, authState: authenticated, onSignedOut: vi.fn(), tab: 'security' })
  const betaReleases = await screen.findByRole('checkbox', { name: 'New beta releases' }) as HTMLInputElement
  await vi.waitFor(() => expect(betaReleases.checked).toBe(true))
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
  render(AccountPage, { account, authState: authenticated, onSignedOut: vi.fn(), tab: 'security' })
  expect((await screen.findByRole('alert')).textContent).toContain('The account service is temporarily unavailable.')
})

test('has no downloads tab because every release is public', async () => {
  mockAccess()
  render(AccountPage, { account, authState: authenticated, onSignedOut: vi.fn() })
  await screen.findByText('Beta access')
  expect(screen.queryByText('Downloads')).toBeNull()
})

test('summarises access on the overview and leaves signing out to the sidebar', async () => {
  mockAccess(bootstrap({
    access: { betaAccess: true, emailVerified: true, downloadsAllowed: true, licences: [{ id: 'licence-test', product: 'Sesame Pro', status: 'active', issuedAt: '2026-08-01T00:00:00Z' }] },
    notificationCounts: { security: 0, support: 3, product: 0 },
  }))
  render(AccountPage, { account, authState: authenticated, onSignedOut: vi.fn() })
  expect(await screen.findByText('3 unread')).toBeTruthy()
  expect(screen.getByText('Granted')).toBeTruthy()
  expect(screen.getByRole('heading', { level: 1, name: 'Overview' })).toBeTruthy()
  expect(screen.queryByRole('button', { name: 'Sign out' })).toBeNull()
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

test('a verified account with no desktop is pointed at connecting one, and the button asks for the Desktops page', async () => {
  mockAccess()
  auth.listDesktopDevices.mockResolvedValue([])
  auth.getDesktopLink.mockResolvedValue(null)
  const onSelectTab = vi.fn()
  render(AccountPage, { account, authState: authenticated, onSignedOut: vi.fn(), onSelectTab })
  expect(await screen.findByRole('heading', { name: 'Connect Sesame on your computer' })).toBeTruthy()
  expect(screen.getByRole('link', { name: 'Download Sesame' }).getAttribute('href')).toBe('https://website.test.invalid/releases')
  expect(screen.getByText('None connected')).toBeTruthy()
  await fireEvent.click(screen.getByRole('button', { name: 'Create a link code' }))
  expect(onSelectTab).toHaveBeenCalledWith('devices')
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

const desktop: DesktopDevice = {
  deviceId: 'device-studio',
  deviceName: 'Studio desktop',
  connectedAt: '2026-11-01T09:00:00Z',
  expiresAt: '2027-11-01T09:00:00Z',
} as DesktopDevice

async function openDesktops() {
  mockAccess()
  auth.listDesktopDevices.mockResolvedValue([desktop])
  auth.getDesktopLink.mockResolvedValue({ state: 'none' })
  render(AccountPage, { account, authState: authenticated, onSignedOut: vi.fn(), tab: 'devices' })
  return (await screen.findByText('Studio desktop')).closest('li') as HTMLElement
}

test('removing a desktop asks first, names it and the consequence, and sends nothing on the first click', async () => {
  const row = await openDesktops()
  await fireEvent.click(within(row).getByRole('button', { name: 'Remove' }))
  expect(row.textContent).toContain('Remove Studio desktop? It is disconnected from this account until you connect it again with a new code. The vault on that computer is not touched.')
  expect(within(row).getByRole('button', { name: 'Remove desktop' })).toBeTruthy()
  expect(auth.reauthenticate).not.toHaveBeenCalled()
  expect(auth.revokeDesktopDevice).not.toHaveBeenCalled()
})

test('cancelling the desktop confirmation puts the row back without a request', async () => {
  const row = await openDesktops()
  await fireEvent.click(within(row).getByRole('button', { name: 'Remove' }))
  await fireEvent.click(within(row).getByRole('button', { name: 'Cancel' }))
  expect(within(row).getByRole('button', { name: 'Remove' })).toBeTruthy()
  expect(within(row).queryByRole('button', { name: 'Remove desktop' })).toBeNull()
  expect(auth.reauthenticate).not.toHaveBeenCalled()
  expect(auth.revokeDesktopDevice).not.toHaveBeenCalled()
})

test('confirming without the password explains it on the row and moves focus to the password field', async () => {
  const row = await openDesktops()
  await fireEvent.click(within(row).getByRole('button', { name: 'Remove' }))
  await fireEvent.click(within(row).getByRole('button', { name: 'Remove desktop' }))
  expect(within(row).getByRole('alert').textContent).toBe('Enter your account password, then remove the desktop again.')
  expect(document.activeElement).toBe(within(row).getByLabelText('Account password'))
  expect(auth.reauthenticate).not.toHaveBeenCalled()
  expect(auth.revokeDesktopDevice).not.toHaveBeenCalled()
})

test('confirming with the password checks it and then removes the desktop', async () => {
  auth.reauthenticate.mockResolvedValue(undefined)
  auth.revokeDesktopDevice.mockResolvedValue(undefined)
  const row = await openDesktops()
  await fireEvent.click(within(row).getByRole('button', { name: 'Remove' }))
  await fireEvent.input(within(row).getByLabelText('Account password'), { target: { value: 'fictional-account-password' } })
  await fireEvent.click(within(row).getByRole('button', { name: 'Remove desktop' }))
  await vi.waitFor(() => expect(screen.queryByText('Studio desktop')).toBeNull())
  expect(auth.reauthenticate).toHaveBeenCalledWith('fictional-account-password')
  expect(auth.revokeDesktopDevice).toHaveBeenCalledWith('device-studio')
})

test('a wrong password is reported on the desktop row that was confirmed', async () => {
  auth.reauthenticate.mockRejectedValue(new Error('That password is not correct.'))
  const row = await openDesktops()
  await fireEvent.click(within(row).getByRole('button', { name: 'Remove' }))
  await fireEvent.input(within(row).getByLabelText('Account password'), { target: { value: 'fictional-wrong-password' } })
  await fireEvent.click(within(row).getByRole('button', { name: 'Remove desktop' }))
  expect((await within(row).findByRole('alert')).textContent).toBe('That password is not correct.')
  expect(auth.revokeDesktopDevice).not.toHaveBeenCalled()
})

const sessions: AccountSession[] = [
  { id: 'session-here', label: 'Firefox on Linux', current: true, lastSeenAt: '2026-11-15T09:00:00Z', expiresAt: '2026-12-15T09:00:00Z' },
  { id: 'session-other', label: 'Safari on macOS', current: false, lastSeenAt: '2026-11-10T09:00:00Z', expiresAt: '2026-12-10T09:00:00Z' },
] as AccountSession[]

async function openSessions() {
  mockAccess()
  auth.listSessions.mockResolvedValue(sessions)
  render(AccountPage, { account, authState: authenticated, onSignedOut: vi.fn(), tab: 'sessions' })
  await screen.findByText('Safari on macOS')
}

test('revoking another session asks first and sends nothing until it is confirmed', async () => {
  await openSessions()
  const row = screen.getByText('Safari on macOS').closest('li') as HTMLElement
  await fireEvent.click(within(row).getByRole('button', { name: 'Revoke' }))
  expect(row.textContent).toContain('Revoke Safari on macOS? That browser is signed out of this account.')
  expect(auth.reauthenticate).not.toHaveBeenCalled()
  expect(auth.revokeSession).not.toHaveBeenCalled()
  await fireEvent.click(within(row).getByRole('button', { name: 'Revoke session' }))
  expect(within(row).getByRole('alert').textContent).toBe('Enter your account password, then revoke the session again.')
  expect(document.activeElement).toBe(within(row).getByLabelText('Account password'))
  expect(auth.revokeSession).not.toHaveBeenCalled()
})

test('revoking the current session says this browser is signed out, and confirming signs out', async () => {
  const assign = vi.fn()
  vi.stubGlobal('location', { assign, search: '', pathname: '/account', hash: '', origin: 'http://localhost', href: 'http://localhost/account' })
  auth.reauthenticate.mockResolvedValue(undefined)
  auth.revokeSession.mockResolvedValue(undefined)
  await openSessions()
  const row = screen.getByText('Firefox on Linux').closest('li') as HTMLElement
  await fireEvent.click(within(row).getByRole('button', { name: 'Revoke' }))
  expect(row.textContent).toContain('Revoke this session? This browser is signed out and goes back to the sign-in page.')
  await fireEvent.input(within(row).getByLabelText('Account password'), { target: { value: 'fictional-account-password' } })
  expect(auth.revokeSession).not.toHaveBeenCalled()
  await fireEvent.click(within(row).getByRole('button', { name: 'Revoke session' }))
  await vi.waitFor(() => expect(assign).toHaveBeenCalledWith('/login'))
  expect(auth.reauthenticate).toHaveBeenCalledWith('fictional-account-password')
  expect(auth.revokeSession).toHaveBeenCalledWith('session-here')
})

test('cancelling a session confirmation keeps the session', async () => {
  await openSessions()
  const row = screen.getByText('Safari on macOS').closest('li') as HTMLElement
  await fireEvent.click(within(row).getByRole('button', { name: 'Revoke' }))
  await fireEvent.click(within(row).getByRole('button', { name: 'Cancel' }))
  expect(within(row).getByRole('button', { name: 'Revoke' })).toBeTruthy()
  expect(auth.revokeSession).not.toHaveBeenCalled()
})

async function openPasskeys() {
  passkey.passkeysSupported.mockReturnValue(true)
  passkey.listPasskeys.mockResolvedValue([{ id: 'passkey-laptop', name: 'Work laptop', createdAt: '2026-10-01T09:00:00Z' }])
  mockAccess()
  auth.getNotificationPreferences.mockResolvedValue({ betaReleases: false, supportReplies: false, productAnnouncements: false })
  render(AccountPage, { account, authState: authenticated, onSignedOut: vi.fn(), tab: 'security' })
  return (await screen.findByText('Work laptop')).closest('li') as HTMLElement
}

test('removing a passkey asks first and points at the password field when it is empty', async () => {
  const row = await openPasskeys()
  await fireEvent.click(within(row).getByRole('button', { name: 'Remove' }))
  expect(row.textContent).toContain('Remove the passkey "Work laptop"? It can no longer sign in to this account.')
  expect(passkey.deletePasskey).not.toHaveBeenCalled()
  await fireEvent.click(within(row).getByRole('button', { name: 'Remove passkey' }))
  expect(within(row).getByRole('alert').textContent).toBe('Enter your account password, then remove the passkey again.')
  expect(document.activeElement).toBe(within(row).getByLabelText('Account password'))
  expect(auth.reauthenticate).not.toHaveBeenCalled()
  expect(passkey.deletePasskey).not.toHaveBeenCalled()
})

test('a confirmed passkey removal checks the password first', async () => {
  auth.reauthenticate.mockResolvedValue(undefined)
  passkey.deletePasskey.mockResolvedValue(undefined)
  const row = await openPasskeys()
  const passkeyForm = screen.getByRole('button', { name: 'Add passkey' }).closest('form') as HTMLElement
  await fireEvent.input(within(passkeyForm).getByLabelText('Account password'), { target: { value: 'fictional-account-password' } })
  await fireEvent.click(within(row).getByRole('button', { name: 'Remove' }))
  await fireEvent.click(within(row).getByRole('button', { name: 'Cancel' }))
  expect(passkey.deletePasskey).not.toHaveBeenCalled()
  await fireEvent.click(within(row).getByRole('button', { name: 'Remove' }))
  await fireEvent.click(within(row).getByRole('button', { name: 'Remove passkey' }))
  await vi.waitFor(() => expect(screen.queryByText('Work laptop')).toBeNull())
  expect(auth.reauthenticate).toHaveBeenCalledWith('fictional-account-password')
  expect(passkey.deletePasskey).toHaveBeenCalledWith('passkey-laptop')
})

test('security sections are headings and the notifications are plain checkboxes with the notice outside them', async () => {
  mockAccess()
  auth.getNotificationPreferences.mockResolvedValue({ betaReleases: false, supportReplies: true, productAnnouncements: false })
  render(AccountPage, { account, authState: authenticated, onSignedOut: vi.fn(), tab: 'security' })
  expect(screen.queryByText('Recent confirmation required')).toBeNull()
  for (const name of ['Sign-in', 'Email notifications']) expect(screen.getByRole('heading', { level: 2, name })).toBeTruthy()
  for (const name of ['Email address', 'Password']) expect(screen.getByRole('heading', { level: 3, name })).toBeTruthy()
  const group = screen.getByRole('group', { name: 'Optional email notifications' })
  expect(within(group).getAllByRole('checkbox')).toHaveLength(3)
  expect(within(group).queryByRole('button')).toBeNull()
  await fireEvent.click(screen.getByRole('button', { name: 'Got it' }))
  expect(screen.queryByText('Reply email is now on for this account.')).toBeNull()
})

test('delete account opens the typed confirmation and keeps the password and DELETE gate', async () => {
  mockAccess()
  render(AccountPage, { account, authState: authenticated, onSignedOut: vi.fn() })
  await screen.findByText('Signed in as')
  const open = screen.getByRole('button', { name: 'Delete account' })
  expect(open.className).toContain('button-danger-text')
  await fireEvent.click(open)
  const submit = screen.getByRole('button', { name: 'Delete permanently' }) as HTMLButtonElement
  expect(submit.disabled).toBe(true)
  await fireEvent.input(screen.getByLabelText('Account password'), { target: { value: 'fictional-account-password' } })
  expect(submit.disabled).toBe(true)
  await fireEvent.input(screen.getByLabelText('Type DELETE'), { target: { value: 'DELETE' } })
  expect(submit.disabled).toBe(false)
  expect(auth.deleteAccount).not.toHaveBeenCalled()
})

test('activity rows read as sentences with the time beside them', async () => {
  mockAccess()
  auth.getAccountActivity.mockResolvedValue([
    { id: 'event-1', type: 'sign_in', label: 'Passkey', metadata: { method: 'passkey' }, createdAt: '2026-11-14T09:41:00Z', expiresAt: '2027-11-14T09:41:00Z' },
    { id: 'event-2', type: 'desktop_revoked', label: 'Sesame desktop', createdAt: '2026-11-13T18:05:00Z', expiresAt: '2027-11-13T18:05:00Z' },
    { id: 'event-3', type: 'something_new', label: 'Sesame account', createdAt: '2026-11-12T08:00:00Z', expiresAt: '2027-11-12T08:00:00Z' },
  ])
  render(AccountPage, { account, authState: authenticated, onSignedOut: vi.fn(), tab: 'activity' })
  expect(await screen.findByText('Signed in with a passkey')).toBeTruthy()
  expect(screen.getByText('Removed a desktop')).toBeTruthy()
  expect(screen.getByText('Something new')).toBeTruthy()
  expect(screen.queryByText('desktop revoked')).toBeNull()
  const time = (screen.getByText('Signed in with a passkey').closest('li') as HTMLElement).querySelector('time')
  expect(time?.getAttribute('datetime')).toBe('2026-11-14T09:41:00Z')
})

test('each page header carries one fact from data the page already loaded', async () => {
  mockAccess()
  auth.getNotificationPreferences.mockResolvedValue({ betaReleases: false, supportReplies: false, productAnnouncements: false })
  const security = render(AccountPage, { account, authState: authenticated, onSignedOut: vi.fn(), tab: 'security' })
  const header = within(screen.getByRole('heading', { level: 1, name: 'Security' }).closest('.page-header') as HTMLElement)
  expect(header.getByText('tester@example.invalid')).toBeTruthy()
  expect(header.getByText('email verified')).toBeTruthy()
  security.unmount()

  auth.listSessions.mockResolvedValue(sessions)
  render(AccountPage, { account, authState: authenticated, onSignedOut: vi.fn(), tab: 'sessions' })
  const sessionsHeader = screen.getByRole('heading', { level: 1, name: 'Sessions' }).closest('.page-header') as HTMLElement
  await vi.waitFor(() => expect(sessionsHeader.textContent).toContain('2 sessions'))
})

test('the email form opens from its row and still confirms the password before asking for the change', async () => {
  mockAccess()
  auth.getNotificationPreferences.mockResolvedValue({ betaReleases: false, supportReplies: false, productAnnouncements: false })
  auth.reauthenticate.mockResolvedValue(undefined)
  auth.requestEmailChange.mockResolvedValue(undefined)
  render(AccountPage, { account, authState: authenticated, onSignedOut: vi.fn(), tab: 'security' })
  const open = screen.getByRole('button', { name: 'Change email' })
  expect(open.getAttribute('aria-expanded')).toBe('false')
  expect(screen.queryByLabelText('New email')).toBeNull()
  await fireEvent.click(open)
  expect(open.getAttribute('aria-expanded')).toBe('true')
  await fireEvent.input(screen.getByLabelText('New email'), { target: { value: 'new@example.invalid' } })
  const form = screen.getByRole('button', { name: 'Send confirmation to new email' }).closest('form') as HTMLElement
  await fireEvent.input(within(form).getByLabelText('Account password'), { target: { value: 'fictional-account-password' } })
  await fireEvent.submit(form)
  await vi.waitFor(() => expect(auth.requestEmailChange).toHaveBeenCalledWith('new@example.invalid'))
  expect(auth.reauthenticate).toHaveBeenCalledWith('fictional-account-password')
  expect(auth.reauthenticate.mock.invocationCallOrder[0]).toBeLessThan(auth.requestEmailChange.mock.invocationCallOrder[0])
})

test('the password form opens from its row and keeps the length and match checks before any request', async () => {
  mockAccess()
  auth.getNotificationPreferences.mockResolvedValue({ betaReleases: false, supportReplies: false, productAnnouncements: false })
  render(AccountPage, { account, authState: authenticated, onSignedOut: vi.fn(), tab: 'security' })
  await fireEvent.click(screen.getByRole('button', { name: 'Change password' }))
  await fireEvent.input(screen.getByLabelText('Current password'), { target: { value: 'fictional-old-password' } })
  await fireEvent.input(screen.getByLabelText('New password'), { target: { value: 'fictional-new-password' } })
  await fireEvent.input(screen.getByLabelText('Confirm new password'), { target: { value: 'fictional-other-password' } })
  await fireEvent.submit(screen.getByRole('button', { name: 'Update password and revoke other sessions' }).closest('form') as HTMLElement)
  expect((await screen.findByRole('alert')).textContent).toBe('The new passwords do not match.')
  expect(auth.reauthenticate).not.toHaveBeenCalled()
  expect(auth.changePassword).not.toHaveBeenCalled()
})

test('a desktop code shows its characters in groups with the time left', async () => {
  mockAccess()
  auth.listDesktopDevices.mockResolvedValue([])
  auth.getDesktopLink.mockResolvedValue({ state: 'none' })
  auth.reauthenticate.mockResolvedValue(undefined)
  auth.createDesktopLink.mockResolvedValue({ state: 'pending', linkId: 'link-test', code: 'SESAME-K7QF2M9XTR4BH8WN', expiresAt: new Date(Date.now() + 600_000).toISOString() })
  render(AccountPage, { account, authState: authenticated, onSignedOut: vi.fn(), tab: 'devices' })
  expect(await screen.findByRole('heading', { name: 'No desktops yet' })).toBeTruthy()
  await fireEvent.input(screen.getByLabelText('Account password'), { target: { value: 'fictional-account-password' } })
  await fireEvent.click(screen.getByRole('button', { name: 'Create desktop code' }))
  await screen.findByText('Desktop code')
  const groups = [...document.querySelectorAll('.code-group')].map((node) => node.textContent)
  expect(groups).toEqual(['K7QF', '2M9X', 'TR4B', 'H8WN'])
  expect(screen.getByText('SESAME-K7QF2M9XTR4BH8WN')).toBeTruthy()
  expect(screen.getByLabelText(/seconds remaining/)).toBeTruthy()
  expect(auth.reauthenticate).toHaveBeenCalledWith('fictional-account-password')
})

test('the Desktops page has no separate password notice, and the password sits beside Create desktop code', async () => {
  const row = await openDesktops()
  expect(screen.queryByText('Account password required')).toBeNull()
  const create = screen.getByRole('button', { name: 'Create desktop code' })
  expect(within(create.parentElement as HTMLElement).getByLabelText('Account password')).toBeTruthy()
  expect(screen.getAllByLabelText('Account password')).toHaveLength(1)
  expect(within(row).queryByLabelText('Account password')).toBeNull()
})

test('the desktop confirm row and the connect block share one password value and send one check before the removal', async () => {
  auth.reauthenticate.mockResolvedValue(undefined)
  auth.revokeDesktopDevice.mockResolvedValue(undefined)
  const row = await openDesktops()
  const connectField = within(screen.getByRole('button', { name: 'Create desktop code' }).parentElement as HTMLElement).getByLabelText('Account password') as HTMLInputElement
  await fireEvent.input(connectField, { target: { value: 'fictional-account-password' } })
  await fireEvent.click(within(row).getByRole('button', { name: 'Remove' }))
  expect((within(row).getByLabelText('Account password') as HTMLInputElement).value).toBe('fictional-account-password')
  await fireEvent.click(within(row).getByRole('button', { name: 'Remove desktop' }))
  await vi.waitFor(() => expect(auth.revokeDesktopDevice).toHaveBeenCalledWith('device-studio'))
  expect(auth.reauthenticate).toHaveBeenCalledOnce()
  expect(auth.reauthenticate.mock.invocationCallOrder[0]).toBeLessThan(auth.revokeDesktopDevice.mock.invocationCallOrder[0])
  await vi.waitFor(() => expect(connectField.value).toBe(''))
})

test('renaming a desktop asks for the password in the same row and checks it before the rename', async () => {
  auth.reauthenticate.mockResolvedValue(undefined)
  auth.renameDesktopDevice.mockResolvedValue(undefined)
  const row = await openDesktops()
  await fireEvent.click(within(row).getByRole('button', { name: 'Rename' }))
  await fireEvent.input(within(row).getByLabelText('Desktop name'), { target: { value: 'Studio tower' } })
  await fireEvent.click(within(row).getByRole('button', { name: 'Save name' }))
  expect((await within(row).findByRole('alert')).textContent).toBe('Enter your website account password to continue.')
  expect(auth.renameDesktopDevice).not.toHaveBeenCalled()
  await fireEvent.input(within(row).getByLabelText('Account password'), { target: { value: 'fictional-account-password' } })
  await fireEvent.click(within(row).getByRole('button', { name: 'Save name' }))
  await vi.waitFor(() => expect(auth.renameDesktopDevice).toHaveBeenCalledWith('device-studio', 'Studio tower'))
  expect(auth.reauthenticate).toHaveBeenCalledWith('fictional-account-password')
})

test('an empty Desktops page says No desktops in the header with nothing under it', async () => {
  mockAccess()
  auth.listDesktopDevices.mockResolvedValue([])
  auth.getDesktopLink.mockResolvedValue({ state: 'none' })
  render(AccountPage, { account, authState: authenticated, onSignedOut: vi.fn(), tab: 'devices' })
  await screen.findByRole('heading', { name: 'No desktops yet' })
  const header = screen.getByRole('heading', { level: 1, name: 'Desktops' }).closest('.page-header') as HTMLElement
  expect(header.querySelector('.page-fact')?.textContent?.trim()).toBe('No desktops')
})

test('the Desktops header says a code is waiting while one is showing', async () => {
  mockAccess()
  auth.listDesktopDevices.mockResolvedValue([])
  auth.getDesktopLink.mockResolvedValue({ state: 'pending', linkId: 'link-test', code: 'SESAME-K7QF2M9XTR4BH8WN', expiresAt: new Date(Date.now() + 600_000).toISOString() })
  render(AccountPage, { account, authState: authenticated, onSignedOut: vi.fn(), tab: 'devices' })
  await screen.findByRole('heading', { name: 'No desktops yet' })
  const header = screen.getByRole('heading', { level: 1, name: 'Desktops' }).closest('.page-header') as HTMLElement
  await vi.waitFor(() => expect(header.querySelector('.page-fact-detail')?.textContent?.trim()).toBe('a code is waiting'))
  expect(header.querySelector('.page-fact-main')?.textContent?.trim()).toBe('No desktops')
})

test('Sessions shows a password field only where a revoke needs it', async () => {
  await openSessions()
  const row = screen.getByText('Safari on macOS').closest('li') as HTMLElement
  expect(screen.queryAllByLabelText('Account password')).toHaveLength(0)
  await fireEvent.click(within(row).getByRole('button', { name: 'Revoke' }))
  expect(screen.getAllByLabelText('Account password')).toHaveLength(1)
  expect(within(row).getByLabelText('Account password')).toBeTruthy()
})

function deferred<T = void>() {
  let resolve!: (value: T) => void
  let reject!: (reason: unknown) => void
  const promise = new Promise<T>((done, fail) => { resolve = done; reject = fail })
  return { promise, resolve, reject }
}

test('a password typed for one session does not skip the confirmation for revoking every session', async () => {
  const assign = vi.fn()
  vi.stubGlobal('location', { assign, search: '', pathname: '/account', hash: '', origin: 'http://localhost', href: 'http://localhost/account' })
  auth.reauthenticate.mockResolvedValue(undefined)
  auth.revokeAllSessions.mockResolvedValue(undefined)
  await openSessions()
  const row = screen.getByText('Safari on macOS').closest('li') as HTMLElement
  await fireEvent.click(within(row).getByRole('button', { name: 'Revoke' }))
  await fireEvent.input(within(row).getByLabelText('Account password'), { target: { value: 'fictional-account-password' } })
  await fireEvent.click(screen.getByRole('button', { name: 'Revoke every session' }))
  expect(auth.reauthenticate).not.toHaveBeenCalled()
  expect(auth.revokeAllSessions).not.toHaveBeenCalled()
  expect(assign).not.toHaveBeenCalled()
  expect(within(row).getByRole('button', { name: 'Revoke' })).toBeTruthy()
  const confirm = screen.getByRole('group', { name: 'Revoke every session?' })
  expect(confirm.textContent).toContain('Every browser is signed out of this account, this one included, and this browser goes back to the sign-in page.')
  expect(screen.getAllByRole('button', { name: 'Revoke every session' })).toHaveLength(1)
  await fireEvent.click(within(confirm).getByRole('button', { name: 'Cancel' }))
  expect(screen.queryByRole('group', { name: 'Revoke every session?' })).toBeNull()
  expect(auth.revokeAllSessions).not.toHaveBeenCalled()
  await fireEvent.click(screen.getByRole('button', { name: 'Revoke every session' }))
  const reopened = screen.getByRole('group', { name: 'Revoke every session?' })
  await fireEvent.click(within(reopened).getByRole('button', { name: 'Revoke every session' }))
  await vi.waitFor(() => expect(assign).toHaveBeenCalledWith('/login'))
  expect(auth.reauthenticate).toHaveBeenCalledWith('fictional-account-password')
  expect(auth.reauthenticate.mock.invocationCallOrder[0]).toBeLessThan(auth.revokeAllSessions.mock.invocationCallOrder[0])
})

test('confirming every session without the password explains it and sends nothing', async () => {
  await openSessions()
  await fireEvent.click(screen.getByRole('button', { name: 'Revoke every session' }))
  const confirm = screen.getByRole('group', { name: 'Revoke every session?' })
  await fireEvent.click(within(confirm).getByRole('button', { name: 'Revoke every session' }))
  expect(within(confirm).getByRole('alert').textContent).toBe('Enter your account password, then revoke every session again.')
  expect(document.activeElement).toBe(within(confirm).getByLabelText('Account password'))
  expect(auth.reauthenticate).not.toHaveBeenCalled()
  expect(auth.revokeAllSessions).not.toHaveBeenCalled()
})

const secondDesktop: DesktopDevice = {
  deviceId: 'device-laptop',
  deviceName: 'Travel laptop',
  connectedAt: '2026-11-02T09:00:00Z',
  expiresAt: '2027-11-02T09:00:00Z',
} as DesktopDevice

async function openTwoDesktops() {
  mockAccess()
  auth.listDesktopDevices.mockResolvedValue([desktop, secondDesktop])
  auth.getDesktopLink.mockResolvedValue({ state: 'none' })
  render(AccountPage, { account, authState: authenticated, onSignedOut: vi.fn(), tab: 'devices' })
  const first = (await screen.findByText('Studio desktop')).closest('li') as HTMLElement
  const second = screen.getByText('Travel laptop').closest('li') as HTMLElement
  return { first, second }
}

test('another desktop row cannot be opened while a removal is in flight, and the result stays on its own row', async () => {
  const removal = deferred()
  auth.reauthenticate.mockResolvedValue(undefined)
  auth.revokeDesktopDevice.mockReturnValue(removal.promise)
  const { first, second } = await openTwoDesktops()
  await fireEvent.click(within(first).getByRole('button', { name: 'Remove' }))
  await fireEvent.input(within(first).getByLabelText('Account password'), { target: { value: 'fictional-account-password' } })
  await fireEvent.click(within(first).getByRole('button', { name: 'Remove desktop' }))
  await vi.waitFor(() => expect(auth.revokeDesktopDevice).toHaveBeenCalledWith('device-studio'))
  const otherRemove = within(second).getByRole('button', { name: 'Remove' }) as HTMLButtonElement
  const otherRename = within(second).getByRole('button', { name: 'Rename' }) as HTMLButtonElement
  expect(otherRemove.disabled).toBe(true)
  expect(otherRename.disabled).toBe(true)
  expect((screen.getByRole('button', { name: 'Create desktop code' }) as HTMLButtonElement).disabled).toBe(true)
  await fireEvent.click(otherRemove)
  await fireEvent.click(otherRename)
  expect(within(second).queryByRole('button', { name: 'Remove desktop' })).toBeNull()
  expect(within(second).queryByLabelText('Desktop name')).toBeNull()
  removal.resolve()
  await vi.waitFor(() => expect(screen.queryByText('Studio desktop')).toBeNull())
  expect(within(second).getByRole('button', { name: 'Remove' })).toBeTruthy()
  await vi.waitFor(() => expect((within(second).getByRole('button', { name: 'Remove' }) as HTMLButtonElement).disabled).toBe(false))
  expect(within(second).queryByRole('button', { name: 'Remove desktop' })).toBeNull()
  expect(auth.revokeDesktopDevice).toHaveBeenCalledOnce()
})

test('a failed desktop removal is reported on its own row and leaves the other row closed', async () => {
  const removal = deferred()
  auth.reauthenticate.mockResolvedValue(undefined)
  auth.revokeDesktopDevice.mockReturnValue(removal.promise)
  const { first, second } = await openTwoDesktops()
  await fireEvent.click(within(first).getByRole('button', { name: 'Remove' }))
  await fireEvent.input(within(first).getByLabelText('Account password'), { target: { value: 'fictional-account-password' } })
  await fireEvent.click(within(first).getByRole('button', { name: 'Remove desktop' }))
  await vi.waitFor(() => expect(auth.revokeDesktopDevice).toHaveBeenCalled())
  await fireEvent.click(within(second).getByRole('button', { name: 'Remove' }))
  removal.reject(new Error('That desktop could not be reached.'))
  expect((await within(first).findByRole('alert')).textContent).toBe('That desktop could not be reached.')
  expect(within(second).queryByRole('alert')).toBeNull()
  expect(within(second).queryByRole('button', { name: 'Remove desktop' })).toBeNull()
  expect((within(first).getByLabelText('Account password') as HTMLInputElement).value).toBe('fictional-account-password')
})

test('another desktop row cannot be opened while a rename is in flight', async () => {
  const rename = deferred()
  auth.reauthenticate.mockResolvedValue(undefined)
  auth.renameDesktopDevice.mockReturnValue(rename.promise)
  const { first, second } = await openTwoDesktops()
  await fireEvent.click(within(first).getByRole('button', { name: 'Rename' }))
  await fireEvent.input(within(first).getByLabelText('Desktop name'), { target: { value: 'Studio tower' } })
  await fireEvent.input(within(first).getByLabelText('Account password'), { target: { value: 'fictional-account-password' } })
  await fireEvent.click(within(first).getByRole('button', { name: 'Save name' }))
  await vi.waitFor(() => expect(auth.renameDesktopDevice).toHaveBeenCalledWith('device-studio', 'Studio tower'))
  expect((within(second).getByRole('button', { name: 'Rename' }) as HTMLButtonElement).disabled).toBe(true)
  expect((within(second).getByRole('button', { name: 'Remove' }) as HTMLButtonElement).disabled).toBe(true)
  await fireEvent.click(within(second).getByRole('button', { name: 'Remove' }))
  expect(within(second).queryByRole('button', { name: 'Remove desktop' })).toBeNull()
  rename.resolve()
  expect(await screen.findByText('Studio tower')).toBeTruthy()
  expect(within(second).queryByLabelText('Desktop name')).toBeNull()
  expect(within(second).queryByRole('button', { name: 'Remove desktop' })).toBeNull()
})

test('another session row cannot be opened while a revocation is in flight', async () => {
  const revocation = deferred()
  auth.reauthenticate.mockResolvedValue(undefined)
  auth.revokeSession.mockReturnValue(revocation.promise)
  await openSessions()
  const other = screen.getByText('Safari on macOS').closest('li') as HTMLElement
  const here = screen.getByText('Firefox on Linux').closest('li') as HTMLElement
  await fireEvent.click(within(other).getByRole('button', { name: 'Revoke' }))
  await fireEvent.input(within(other).getByLabelText('Account password'), { target: { value: 'fictional-account-password' } })
  await fireEvent.click(within(other).getByRole('button', { name: 'Revoke session' }))
  await vi.waitFor(() => expect(auth.revokeSession).toHaveBeenCalledWith('session-other'))
  expect((within(here).getByRole('button', { name: 'Revoke' }) as HTMLButtonElement).disabled).toBe(true)
  expect((screen.getByRole('button', { name: 'Revoke every session' }) as HTMLButtonElement).disabled).toBe(true)
  await fireEvent.click(within(here).getByRole('button', { name: 'Revoke' }))
  await fireEvent.click(screen.getByRole('button', { name: 'Revoke every session' }))
  expect(within(here).queryByRole('button', { name: 'Revoke session' })).toBeNull()
  expect(screen.queryByRole('group', { name: 'Revoke every session?' })).toBeNull()
  revocation.reject(new Error('That session could not be revoked right now.'))
  expect((await within(other).findByRole('alert')).textContent).toBe('That session could not be revoked right now.')
  expect(within(here).queryByRole('alert')).toBeNull()
  expect(within(here).getByRole('button', { name: 'Revoke' })).toBeTruthy()
  expect(auth.revokeSession).toHaveBeenCalledOnce()
  expect(auth.revokeAllSessions).not.toHaveBeenCalled()
})
