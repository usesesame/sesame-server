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
  conditionalPasskeysAvailable: vi.fn(async () => false),
  signInWithPasskey: vi.fn(),
}))

vi.mock('../src/lib/auth', () => auth)
vi.mock('../src/lib/passkey', () => passkey)
vi.mock('../src/lib/runtime-config', () => ({ siteOrigin: 'https://website.test.invalid' }))

import AuthPage from '../src/pages/AuthPage.svelte'

const account = { id: 'account-test', email: 'tester@example.invalid', emailVerified: true, betaAccess: true }
const open = { mode: 'invite', enabled: true, requiresInvite: true }

let assign: ReturnType<typeof vi.fn>

function stubLocation(search = '') {
  const mock = vi.fn()
  vi.stubGlobal('location', { assign: mock, search, pathname: '/', hash: '', origin: 'http://localhost', href: 'http://localhost/' })
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
  passkey.passkeysSupported.mockReset().mockReturnValue(false)
  passkey.conditionalPasskeysAvailable.mockReset().mockResolvedValue(false)
  vi.unstubAllGlobals()
})

async function fillRegistration(user: ReturnType<typeof userEvent.setup>, code?: string) {
  await user.type(screen.getByLabelText('Email'), 'tester@example.invalid')
  if (code) await user.type(screen.getByLabelText('Invitation code'), code)
  await user.type(screen.getByLabelText('Create a password'), 'correct horse battery staple')
}

test('signs in with the website account credentials', async () => {
  const onAuthenticated = vi.fn()
  auth.signIn.mockResolvedValue(account)
  render(AuthPage, { mode: 'login', onAuthenticated })
  const user = userEvent.setup()
  await user.type(screen.getByLabelText('Email'), 'tester@example.invalid')
  await user.type(screen.getByLabelText('Password'), 'correct horse battery staple')
  await user.click(screen.getByRole('button', { name: 'Sign in' }))
  await vi.waitFor(() => expect(onAuthenticated).toHaveBeenCalledWith(account))
  expect(auth.signIn).toHaveBeenCalledWith('tester@example.invalid', 'correct horse battery staple')
  expect(assign).toHaveBeenCalledWith('/account')
  expect(screen.queryByRole('alert')).toBeNull()
})

test('shows the sign-in failure, clears the password and moves focus to the message', async () => {
  auth.signIn.mockRejectedValue(new Error('Email or password is incorrect.'))
  render(AuthPage, { mode: 'login', onAuthenticated: vi.fn() })
  await fireEvent.input(screen.getByLabelText('Email'), { target: { value: 'tester@example.invalid' } })
  await fireEvent.input(screen.getByLabelText('Password'), { target: { value: 'wrong password' } })
  await fireEvent.click(screen.getByRole('button', { name: 'Sign in' }))
  const alert = await screen.findByRole('alert')
  expect(alert.textContent).toContain('Email or password is incorrect.')
  expect((screen.getByLabelText('Password') as HTMLInputElement).value).toBe('')
  await vi.waitFor(() => expect(document.activeElement).toBe(alert))
})

test('the sign-in page offers recovery and sign-up where a person looks for them', () => {
  render(AuthPage, { mode: 'login', onAuthenticated: vi.fn() })
  expect(screen.getByRole('link', { name: 'Forgot password?' }).getAttribute('href')).toBe('/forgot-password')
  expect(screen.getByRole('link', { name: 'Create an account' }).getAttribute('href')).toBe('/register')
})

test('the password name does not include the show button, and the button says what it does', async () => {
  render(AuthPage, { mode: 'login', onAuthenticated: vi.fn() })
  const field = screen.getByLabelText('Password') as HTMLInputElement
  expect(field.type).toBe('password')
  await fireEvent.click(screen.getByRole('button', { name: 'Show password' }))
  expect(field.type).toBe('text')
  expect(screen.getByRole('button', { name: 'Hide password' })).toBeTruthy()
})

test('creates an invited account without a session and asks for email verification', async () => {
  auth.getRegistrationStatus.mockResolvedValue(open)
  auth.register.mockResolvedValue(undefined)
  const onAuthenticated = vi.fn()
  render(AuthPage, { mode: 'register', onAuthenticated })
  await screen.findByLabelText('Invitation code')
  const user = userEvent.setup()
  await fillRegistration(user, 'INVITE-TEST')
  const submit = screen.getByRole('button', { name: 'Create account' }) as HTMLButtonElement
  expect(submit.disabled).toBe(false)
  await user.click(screen.getByRole('checkbox'))
  await user.click(submit)
  expect(await screen.findByText('Check your email')).toBeTruthy()
  expect(screen.getByText(/tester@example.invalid/)).toBeTruthy()
  expect(onAuthenticated).not.toHaveBeenCalled()
  expect(assign).not.toHaveBeenCalled()
  expect(auth.register).toHaveBeenCalledWith('tester@example.invalid', 'correct horse battery staple', 'INVITE-TEST', {
    termsAccepted: true,
    termsVersion: LEGAL_VERSION,
    privacyAcknowledged: true,
    privacyVersion: LEGAL_VERSION,
  })
})

test('a missing agreement is explained next to the box and sends nothing, instead of leaving a dead button', async () => {
  auth.getRegistrationStatus.mockResolvedValue(open)
  render(AuthPage, { mode: 'register', onAuthenticated: vi.fn() })
  await screen.findByLabelText('Invitation code')
  const user = userEvent.setup()
  await fillRegistration(user, 'INVITE-TEST')
  await user.click(screen.getByRole('button', { name: 'Create account' }))
  expect(auth.register).not.toHaveBeenCalled()
  const box = screen.getByRole('checkbox')
  expect(screen.getByRole('alert').textContent).toContain('Accept the Terms of Use')
  expect(box.getAttribute('aria-describedby')).toBe(screen.getByRole('alert').id)
  expect(document.activeElement).toBe(box)
})

test('shows the registration failure without claiming an account was created', async () => {
  auth.getRegistrationStatus.mockResolvedValue(open)
  auth.register.mockRejectedValue(new Error('This beta invitation is unavailable or does not match that email address.'))
  render(AuthPage, { mode: 'register', onAuthenticated: vi.fn() })
  await screen.findByLabelText('Invitation code')
  const user = userEvent.setup()
  await fillRegistration(user, 'INVITE-TEST')
  await user.click(screen.getByRole('checkbox'))
  await user.click(screen.getByRole('button', { name: 'Create account' }))
  expect((await screen.findByRole('alert')).textContent).toContain('This beta invitation is unavailable or does not match that email address.')
  expect(screen.queryByText('Check your email')).toBeNull()
})

test('an invitation link fills the code in and says so, with a way to use another code', async () => {
  stubLocation('?invite=LINK-CODE')
  auth.getRegistrationStatus.mockResolvedValue(open)
  auth.register.mockResolvedValue(undefined)
  render(AuthPage, { mode: 'register', onAuthenticated: vi.fn() })
  expect(await screen.findByText('Invitation applied')).toBeTruthy()
  expect(screen.queryByLabelText('Invitation code')).toBeNull()
  const user = userEvent.setup()
  await fillRegistration(user)
  await user.click(screen.getByRole('checkbox'))
  await user.click(screen.getByRole('button', { name: 'Create account' }))
  await screen.findByText('Check your email')
  expect(auth.register.mock.calls[0][2]).toBe('LINK-CODE')
})

test('using another code shows the field with the linked code in it', async () => {
  stubLocation('?invite=LINK-CODE')
  auth.getRegistrationStatus.mockResolvedValue(open)
  render(AuthPage, { mode: 'register', onAuthenticated: vi.fn() })
  await fireEvent.click(await screen.findByRole('button', { name: 'Use another code' }))
  expect((screen.getByLabelText('Invitation code') as HTMLInputElement).value).toBe('LINK-CODE')
  expect(screen.queryByText('Invitation applied')).toBeNull()
})

test('says the password length before it is typed and counts toward it', async () => {
  auth.getRegistrationStatus.mockResolvedValue(open)
  render(AuthPage, { mode: 'register', onAuthenticated: vi.fn() })
  await screen.findByLabelText('Invitation code')
  expect(screen.getByText(/12 characters or more/)).toBeTruthy()
  await fireEvent.input(screen.getByLabelText('Create a password'), { target: { value: 'short' } })
  expect(screen.getByText('5 of 12 characters')).toBeTruthy()
  await fireEvent.input(screen.getByLabelText('Create a password'), { target: { value: 'long enough now' } })
  expect(screen.getByText('Long enough')).toBeTruthy()
})

test('shows the invite-only notice when registration is closed, with a way to ask for an invitation', async () => {
  auth.getRegistrationStatus.mockResolvedValue({ mode: 'closed', enabled: false, requiresInvite: false })
  render(AuthPage, { mode: 'register', onAuthenticated: vi.fn() })
  expect(await screen.findByText('Accounts are invite-only for now')).toBeTruthy()
  expect(screen.queryByRole('button', { name: 'Create account' })).toBeNull()
  expect(screen.getByRole('link', { name: 'Ask for an invitation' }).getAttribute('href')).toBe('/support/request')
  expect(screen.getByRole('link', { name: 'Back to sign in' })).toBeTruthy()
})

test('a failed registration check says so and offers a retry instead of claiming the beta is closed', async () => {
  auth.getRegistrationStatus.mockRejectedValueOnce(new Error('The account service could not be reached.'))
  render(AuthPage, { mode: 'register', onAuthenticated: vi.fn() })
  expect((await screen.findByRole('alert')).textContent).toContain('The account service could not be reached.')
  expect(screen.queryByText('Accounts are invite-only for now')).toBeNull()
  auth.getRegistrationStatus.mockResolvedValue(open)
  await fireEvent.click(screen.getByRole('button', { name: 'Try again' }))
  expect(await screen.findByLabelText('Invitation code')).toBeTruthy()
  expect(auth.getRegistrationStatus).toHaveBeenCalledTimes(2)
})

test('where the browser supports it the email field offers passkeys in autofill and starts a quiet request', async () => {
  passkey.passkeysSupported.mockReturnValue(true)
  passkey.conditionalPasskeysAvailable.mockResolvedValue(true)
  passkey.signInWithPasskey.mockReturnValue(new Promise(() => {}))
  render(AuthPage, { mode: 'login', onAuthenticated: vi.fn() })
  await vi.waitFor(() => expect(passkey.signInWithPasskey).toHaveBeenCalledTimes(1))
  expect(passkey.signInWithPasskey.mock.calls[0][0]).toMatchObject({ conditional: true })
  expect(screen.getByLabelText('Email').getAttribute('autocomplete')).toBe('username webauthn')
})

test('choosing a passkey from autofill signs in', async () => {
  passkey.passkeysSupported.mockReturnValue(true)
  passkey.conditionalPasskeysAvailable.mockResolvedValue(true)
  passkey.signInWithPasskey.mockResolvedValue(account)
  const onAuthenticated = vi.fn()
  render(AuthPage, { mode: 'login', onAuthenticated })
  await vi.waitFor(() => expect(onAuthenticated).toHaveBeenCalledWith(account))
  expect(assign).toHaveBeenCalledWith('/account')
})

test('a quiet passkey request that fails or is dismissed shows no error', async () => {
  passkey.passkeysSupported.mockReturnValue(true)
  passkey.conditionalPasskeysAvailable.mockResolvedValue(true)
  passkey.signInWithPasskey.mockRejectedValue(new Error('Passkey sign-in was cancelled.'))
  render(AuthPage, { mode: 'login', onAuthenticated: vi.fn() })
  await vi.waitFor(() => expect(passkey.signInWithPasskey).toHaveBeenCalled())
  await Promise.resolve()
  expect(screen.queryByRole('alert')).toBeNull()
})

test('pressing the passkey button stops the quiet request and asks openly', async () => {
  passkey.passkeysSupported.mockReturnValue(true)
  passkey.conditionalPasskeysAvailable.mockResolvedValue(true)
  passkey.signInWithPasskey.mockReturnValueOnce(new Promise(() => {})).mockResolvedValue(account)
  const onAuthenticated = vi.fn()
  render(AuthPage, { mode: 'login', onAuthenticated })
  await vi.waitFor(() => expect(passkey.signInWithPasskey).toHaveBeenCalledTimes(1))
  const quiet = passkey.signInWithPasskey.mock.calls[0][0] as { signal: AbortSignal }
  await fireEvent.click(screen.getByRole('button', { name: 'Sign in with a passkey' }))
  expect(quiet.signal.aborted).toBe(true)
  await vi.waitFor(() => expect(onAuthenticated).toHaveBeenCalledWith(account))
  expect(passkey.signInWithPasskey.mock.calls[1][0]?.conditional).toBeFalsy()
})

test('without conditional support the email field keeps a plain email autofill and the button still works', async () => {
  passkey.passkeysSupported.mockReturnValue(true)
  render(AuthPage, { mode: 'login', onAuthenticated: vi.fn() })
  await Promise.resolve()
  expect(screen.getByLabelText('Email').getAttribute('autocomplete')).toBe('email')
  expect(passkey.signInWithPasskey).not.toHaveBeenCalled()
  expect(screen.getByRole('button', { name: 'Sign in with a passkey' })).toBeTruthy()
})
