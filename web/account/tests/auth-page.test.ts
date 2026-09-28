import { cleanup, fireEvent, render, screen } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { LEGAL_VERSION } from '../src/lib/legal'

const auth = vi.hoisted(() => ({
  getRegistrationStatus: vi.fn(),
  register: vi.fn(),
  signIn: vi.fn(),
}))

const passkey = vi.hoisted(() => ({
  passkeysSupported: vi.fn(() => false),
  signInWithPasskey: vi.fn(),
}))

vi.mock('../src/lib/auth', () => auth)
vi.mock('../src/lib/passkey', () => passkey)
vi.mock('../src/lib/runtime-config', () => ({ siteOrigin: 'https://website.test.invalid' }))

import AuthPage from '../src/pages/AuthPage.svelte'

const account = { id: 'account-test', email: 'tester@example.invalid', emailVerified: true, betaAccess: true }

let assign: ReturnType<typeof vi.fn>

function stubLocation() {
  const mock = vi.fn()
  vi.stubGlobal('location', { assign: mock, search: '', pathname: '/', hash: '', origin: 'http://localhost', href: 'http://localhost/' })
  return mock
}

beforeEach(() => {
  assign = stubLocation()
})

afterEach(() => {
  cleanup()
  auth.getRegistrationStatus.mockReset()
  auth.register.mockReset()
  auth.signIn.mockReset()
  passkey.signInWithPasskey.mockReset()
  vi.unstubAllGlobals()
})

test('signs in with the website account credentials', async () => {
  const onAuthenticated = vi.fn()
  auth.signIn.mockResolvedValue(account)
  render(AuthPage, { mode: 'login', onAuthenticated })
  const user = userEvent.setup()
  await user.type(screen.getByLabelText('Email'), 'tester@example.invalid')
  await user.type(screen.getByLabelText(/password/i), 'correct horse battery staple')
  await user.click(screen.getByRole('button', { name: 'Sign in' }))
  await vi.waitFor(() => expect(onAuthenticated).toHaveBeenCalledWith(account))
  expect(auth.signIn).toHaveBeenCalledWith('tester@example.invalid', 'correct horse battery staple')
  expect(assign).toHaveBeenCalledWith('/account')
  expect(screen.queryByRole('alert')).toBeNull()
})

test('shows the sign-in failure and clears the password', async () => {
  auth.signIn.mockRejectedValue(new Error('Email or password is incorrect.'))
  render(AuthPage, { mode: 'login', onAuthenticated: vi.fn() })
  await fireEvent.input(screen.getByLabelText('Email'), { target: { value: 'tester@example.invalid' } })
  await fireEvent.input(screen.getByLabelText(/password/i), { target: { value: 'wrong password' } })
  await fireEvent.click(screen.getByRole('button', { name: 'Sign in' }))
  expect((await screen.findByRole('alert')).textContent).toContain('Email or password is incorrect.')
  expect((screen.getByLabelText(/password/i) as HTMLInputElement).value).toBe('')
})

test('requires legal acceptance before creating an invited account', async () => {
  auth.getRegistrationStatus.mockResolvedValue({ mode: 'invite', enabled: true, requiresInvite: true })
  auth.register.mockResolvedValue(account)
  const onAuthenticated = vi.fn()
  render(AuthPage, { mode: 'register', onAuthenticated })
  await screen.findByLabelText('Invitation code')
  await fireEvent.input(screen.getByLabelText('Email'), { target: { value: 'tester@example.invalid' } })
  await fireEvent.input(screen.getByLabelText('Invitation code'), { target: { value: 'INVITE-TEST' } })
  await fireEvent.input(screen.getByLabelText(/password/i), { target: { value: 'correct horse battery staple' } })
  const submit = screen.getByRole('button', { name: 'Create beta account' }) as HTMLButtonElement
  expect(submit.disabled).toBe(true)
  await fireEvent.click(submit)
  expect(auth.register).not.toHaveBeenCalled()
  await fireEvent.click(screen.getByRole('checkbox'))
  expect(submit.disabled).toBe(false)
  await fireEvent.click(submit)
  await vi.waitFor(() => expect(onAuthenticated).toHaveBeenCalledWith(account))
  expect(auth.register).toHaveBeenCalledWith('tester@example.invalid', 'correct horse battery staple', 'INVITE-TEST', {
    termsAccepted: true,
    termsVersion: LEGAL_VERSION,
    privacyAcknowledged: true,
    privacyVersion: LEGAL_VERSION,
  })
})

test('shows the invite-only notice when registration is closed', async () => {
  auth.getRegistrationStatus.mockResolvedValue({ mode: 'closed', enabled: false, requiresInvite: false })
  render(AuthPage, { mode: 'register', onAuthenticated: vi.fn() })
  expect(await screen.findByText('Accounts are invite-only')).toBeTruthy()
  expect(screen.queryByRole('button', { name: 'Create beta account' })).toBeNull()
  expect(screen.getByRole('link', { name: 'Back to sign in' })).toBeTruthy()
})
