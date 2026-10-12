import { cleanup, fireEvent, render, screen } from '@testing-library/svelte'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'

const auth = vi.hoisted(() => ({
  confirmEmailChange: vi.fn(),
  confirmEmailVerification: vi.fn(),
  confirmPasswordRecovery: vi.fn(),
  requestPasswordRecovery: vi.fn(),
  setupPassword: vi.fn(),
}))

vi.mock('../src/lib/auth', () => auth)

import AccountFlowPage from '../src/pages/AccountFlowPage.svelte'

function apiFailure(message: string, code: string) {
  return Object.assign(new Error(message), { code })
}

const account = { id: 'account-test', email: 'tester@example.invalid', emailVerified: true, betaAccess: true }

beforeEach(() => {
  window.history.replaceState({}, '', `/verify-email#token=${'t'.repeat(40)}`)
})

afterEach(() => {
  cleanup()
  Object.values(auth).forEach((mock) => mock.mockReset())
  window.history.replaceState({}, '', '/')
})

async function verify() {
  await fireEvent.click(screen.getByRole('button', { name: 'Verify email' }))
}

async function fillPasswords(first: string, second: string) {
  await fireEvent.input(screen.getAllByLabelText(/new password/i)[0], { target: { value: first } })
  await fireEvent.input(screen.getByLabelText('Confirm new password'), { target: { value: second } })
}

test('asks for a password after the first verification and finishes once it is set', async () => {
  auth.confirmEmailVerification.mockResolvedValue({ account, credentialSetupRequired: true })
  auth.setupPassword.mockResolvedValue(undefined)
  const onAuthenticated = vi.fn()
  render(AccountFlowPage, { mode: 'verify-email', onAuthenticated })
  await verify()

  expect(await screen.findByRole('heading', { name: 'Choose a password' })).toBeTruthy()
  expect(onAuthenticated).toHaveBeenCalledWith(account)
  expect(screen.queryByRole('link', { name: 'Open account' })).toBeNull()
  expect(screen.getByRole('link', { name: 'add a passkey' }).getAttribute('href')).toBe('/account#security')

  await fillPasswords('fictional-first-password', 'fictional-first-password')
  await fireEvent.click(screen.getByRole('button', { name: 'Set password' }))

  expect(auth.setupPassword).toHaveBeenCalledWith('fictional-first-password')
  expect(await screen.findByRole('heading', { name: 'Password set' })).toBeTruthy()
  expect(screen.getByRole('link', { name: 'Open account' }).getAttribute('href')).toBe('/account')
})

test('skips the password step when the account keeps its credentials', async () => {
  auth.confirmEmailVerification.mockResolvedValue({ account, credentialSetupRequired: false })
  render(AccountFlowPage, { mode: 'verify-email', onAuthenticated: vi.fn() })
  await verify()

  expect(await screen.findByRole('heading', { name: 'Email verified' })).toBeTruthy()
  expect(screen.queryByRole('heading', { name: 'Choose a password' })).toBeNull()
  expect(screen.getByRole('link', { name: 'Open account' })).toBeTruthy()
})

test('does not send mismatched passwords', async () => {
  auth.confirmEmailVerification.mockResolvedValue({ account, credentialSetupRequired: true })
  render(AccountFlowPage, { mode: 'verify-email', onAuthenticated: vi.fn() })
  await verify()
  await screen.findByRole('heading', { name: 'Choose a password' })

  await fillPasswords('fictional-first-password', 'fictional-other-password')
  await fireEvent.click(screen.getByRole('button', { name: 'Set password' }))

  expect((await screen.findByRole('alert')).textContent).toContain('The new passwords do not match.')
  expect(auth.setupPassword).not.toHaveBeenCalled()
})

test('points to password recovery when the sign-in is too old', async () => {
  auth.confirmEmailVerification.mockResolvedValue({ account, credentialSetupRequired: true })
  auth.setupPassword.mockRejectedValue(apiFailure('Confirm your account password before making this change.', 'recent_auth_required'))
  render(AccountFlowPage, { mode: 'verify-email', onAuthenticated: vi.fn() })
  await verify()
  await screen.findByRole('heading', { name: 'Choose a password' })

  await fillPasswords('fictional-first-password', 'fictional-first-password')
  await fireEvent.click(screen.getByRole('button', { name: 'Set password' }))

  expect((await screen.findByRole('alert')).textContent).toContain('This sign-in is too old to set a password.')
  expect(screen.getByRole('link', { name: 'send yourself a recovery link' }).getAttribute('href')).toBe('/forgot-password')
  expect(screen.getByRole('heading', { name: 'Choose a password' })).toBeTruthy()
})

test('shows the service message when setting the password fails', async () => {
  auth.confirmEmailVerification.mockResolvedValue({ account, credentialSetupRequired: true })
  auth.setupPassword.mockRejectedValue(apiFailure('This account already has a password. Change it from the security settings.', 'password_already_set'))
  render(AccountFlowPage, { mode: 'verify-email', onAuthenticated: vi.fn() })
  await verify()
  await screen.findByRole('heading', { name: 'Choose a password' })

  await fillPasswords('fictional-first-password', 'fictional-first-password')
  await fireEvent.click(screen.getByRole('button', { name: 'Set password' }))

  expect((await screen.findByRole('alert')).textContent).toContain('This account already has a password.')
  expect(screen.queryByRole('link', { name: 'send yourself a recovery link' })).toBeNull()
})
