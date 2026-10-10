import { fireEvent, screen, waitFor, within } from '@testing-library/svelte'
import { expect, test } from 'vitest'
import { apiError, session } from './support/fake-api'
import { config, openApp, storedText, useCleanApp } from './support/app'

useCleanApp()

const PASSWORD = 'fictional password for tests'

async function signInForm(code = '123456', password = PASSWORD) {
  await fireEvent.input(await screen.findByLabelText('Name'), { target: { value: 'Alex Rivera' } })
  await fireEvent.input(screen.getByLabelText('Password'), { target: { value: password } })
  await fireEvent.input(screen.getByLabelText('Six-digit code'), { target: { value: code } })
  await fireEvent.click(screen.getByRole('button', { name: 'Sign in' }))
}

test('shows the sign-in form when there is no session and signs in', async () => {
  let signedIn = false
  const { calls } = openApp('/', {
    'GET /v1/owner/session': () => (signedIn ? session : apiError(401, 'not_authenticated', 'Sign in to continue.')),
    'POST /v1/owner/login': () => { signedIn = true; return session },
  })
  expect(await screen.findByRole('heading', { name: 'Sign in' })).toBeTruthy()
  await signInForm()
  expect(await screen.findByRole('heading', { name: 'Devices' })).toBeTruthy()
  const login = calls.find((call) => call.path === '/v1/owner/login')!
  expect(login.body).toEqual({ name: 'Alex Rivera', password: PASSWORD, code: '123456' })
  expect(storedText()).not.toContain(PASSWORD)
  expect(storedText()).not.toContain('csrf-one')
})

test('shows the server message after a failed sign-in and clears the code', async () => {
  openApp('/', {
    'GET /v1/owner/session': () => apiError(401, 'not_authenticated', 'Sign in to continue.'),
    'POST /v1/owner/login': () => apiError(401, 'invalid_credentials', 'The name, password or code is incorrect.'),
  })
  await signInForm('000000', 'wrong password here')
  expect((await screen.findByRole('alert')).textContent).toBe('The name, password or code is incorrect.')
  expect((screen.getByLabelText('Six-digit code') as HTMLInputElement).value).toBe('')
  expect(screen.queryByRole('heading', { name: 'Devices' })).toBeNull()
})

test('shows the rate limit message from the server', async () => {
  openApp('/', {
    'GET /v1/owner/session': () => apiError(401, 'not_authenticated', 'Sign in to continue.'),
    'POST /v1/owner/login': () => apiError(429, 'too_many_attempts', 'Try again later.'),
  })
  await signInForm()
  expect((await screen.findByRole('alert')).textContent).toBe('Try again later.')
})

test('points to the setup link when the server has no owner yet', async () => {
  openApp('/', {
    'GET /config.json': () => ({ ...config, setupRequired: true }),
    'GET /v1/owner/session': () => apiError(401, 'not_authenticated', 'Sign in to continue.'),
  })
  expect(await screen.findByText(/No owner exists yet/)).toBeTruthy()
  expect(screen.queryByLabelText('Password')).toBeNull()
})

test('shows the server message when the console is opened at the wrong host', async () => {
  openApp('/', { 'GET /v1/owner/session': () => apiError(421, 'host_mismatch', 'This server accepts sign-in and pairing requests only at sesame.example.test.') })
  expect(await screen.findByRole('heading', { name: 'The console cannot reach the server' })).toBeTruthy()
  expect(screen.getByText('This server accepts sign-in and pairing requests only at sesame.example.test.')).toBeTruthy()
})

test('says so when the server cannot be reached at all', async () => {
  openApp('/', { 'GET /config.json': () => apiError(503, 'unavailable', 'Down.') })
  expect(await screen.findByRole('heading', { name: 'The console cannot reach the server' })).toBeTruthy()
})

test('returns to sign-in with a notice when the session ends while the console is open', async () => {
  let expired = false
  openApp('/', { 'GET /v1/owner/devices': () => (expired ? apiError(401, 'session_expired', 'Your session has expired. Sign in to continue.') : { devices: [] }) })
  await screen.findByRole('heading', { name: 'Devices' })
  expired = true
  await fireEvent.click(screen.getByRole('link', { name: 'Members' }))
  await screen.findByRole('heading', { name: 'Members' })
  await fireEvent.click(screen.getByRole('link', { name: 'Devices' }))
  expect(await screen.findByRole('heading', { name: 'Sign in' })).toBeTruthy()
  expect(screen.getByText('Your session ended.')).toBeTruthy()
})

test('signing out calls the server, forgets the token and shows the sign-in form', async () => {
  let signedOut = false
  const { calls } = openApp('/', {
    'GET /v1/owner/session': () => (signedOut ? apiError(401, 'not_authenticated', 'Sign in to continue.') : session),
    'POST /v1/owner/logout': () => { signedOut = true; return undefined },
  })
  await screen.findByRole('heading', { name: 'Devices' })
  await fireEvent.click(screen.getByRole('button', { name: 'Sign out' }))
  expect(await screen.findByRole('heading', { name: 'Sign in' })).toBeTruthy()
  const logout = calls.find((call) => call.path === '/v1/owner/logout')!
  expect(logout.headers.get('X-Sesame-CSRF')).toBe('csrf-one')
  expect(screen.queryByText('Rivera household')).toBeNull()
})

test('navigates between screens with the address bar hash', async () => {
  openApp('/')
  await screen.findByRole('heading', { name: 'Devices' })
  await fireEvent.click(screen.getByRole('link', { name: 'System' }))
  expect(await screen.findByRole('heading', { name: 'System' })).toBeTruthy()
  expect(window.location.hash).toBe('#/system')
  const nav = screen.getByRole('navigation', { name: 'Console' })
  await waitFor(() => expect(within(nav).getByRole('link', { name: 'System' }).getAttribute('aria-current')).toBe('page'))
  expect(within(nav).queryByText(/support|plans|releases|extension/i)).toBeNull()
})

test('shows the pair link page without calling the server and removes the code from the address bar', async () => {
  const { calls } = openApp('/pair#code=fictionalpairingcode&fp=abcdef')
  expect(await screen.findByRole('heading', { name: 'Open this link in the Sesame app' })).toBeTruthy()
  expect(window.location.href).not.toContain('fictionalpairingcode')
  expect(calls).toHaveLength(0)
})
