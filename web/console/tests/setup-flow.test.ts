import { fireEvent, screen, waitFor } from '@testing-library/svelte'
import { expect, test } from 'vitest'
import { apiError, session } from './support/fake-api'
import { config, openApp, storedText, useCleanApp } from './support/app'

useCleanApp()

const TOKEN = 'fictional-setup-token-0123456789abcdef'
const SECRET = 'JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP'
const details = { totpSecret: SECRET, totpUri: `otpauth://totp/Sesame:owner?secret=${SECRET}&issuer=Sesame`, ownerName: '', firstOwner: true, expiresAt: '2099-01-01T00:00:00Z' }
const PASSWORD = 'fictional password for tests'

async function fillForm(code = '123456', repeat = PASSWORD) {
  await fireEvent.input(await screen.findByLabelText('Name'), { target: { value: 'Alex Rivera' } })
  await fireEvent.input(screen.getByLabelText(/^Password/), { target: { value: PASSWORD } })
  await fireEvent.input(screen.getByLabelText('Repeat the password'), { target: { value: repeat } })
  await fireEvent.input(screen.getByLabelText('Six-digit code'), { target: { value: code } })
}

test('reads the token from the fragment, removes it from the address bar and shows the QR code and key', async () => {
  const { calls } = openApp(`/setup#token=${TOKEN}`, {
    'GET /config.json': () => ({ ...config, setupRequired: true }),
    'POST /v1/owner/setup/details': () => details,
  })
  expect(await screen.findByRole('heading', { name: 'Create the first owner' })).toBeTruthy()
  expect(window.location.href).not.toContain(TOKEN)
  expect(window.location.hash).toBe('')
  expect(window.location.pathname).toBe('/setup')
  const detailsCall = calls.find((call) => call.path === '/v1/owner/setup/details')!
  expect(detailsCall.body).toEqual({ token: TOKEN })
  expect(detailsCall.headers.get('X-Sesame-CSRF')).toBeNull()
  const qr = screen.getByRole('img', { name: 'Authenticator setup code' })
  expect(qr.querySelector('path')?.getAttribute('d')?.startsWith('M4 4h7')).toBe(true)
  expect(screen.getByText(SECRET)).toBeTruthy()
  expect(storedText()).not.toContain(SECRET)
  expect(storedText()).not.toContain(TOKEN)
})

test('keeps the button off until the password is long enough, matches and the code has six digits', async () => {
  openApp(`/setup#token=${TOKEN}`, { 'GET /config.json': () => ({ ...config, setupRequired: true }), 'POST /v1/owner/setup/details': () => details })
  const button = (await screen.findByRole('button', { name: 'Create owner' })) as HTMLButtonElement
  expect(button.disabled).toBe(true)
  await fillForm('123456', 'a different password')
  expect(screen.getByText('The two passwords do not match.')).toBeTruthy()
  expect(button.disabled).toBe(true)
  await fireEvent.input(screen.getByLabelText('Repeat the password'), { target: { value: PASSWORD } })
  expect(button.disabled).toBe(false)
  await fireEvent.input(screen.getByLabelText('Six-digit code'), { target: { value: '12345' } })
  expect(button.disabled).toBe(true)
  await fireEvent.input(screen.getByLabelText(/^Password/), { target: { value: 'short' } })
  expect(screen.getByText('At least 12 characters.')).toBeTruthy()
  expect(button.disabled).toBe(true)
})

test('creates the owner and opens the console', async () => {
  let created = false
  const { calls } = openApp(`/setup#token=${TOKEN}`, {
    'GET /config.json': () => ({ ...config, setupRequired: true }),
    'POST /v1/owner/setup/details': () => details,
    'POST /v1/owner/setup': () => { created = true; return new Response(JSON.stringify(session), { status: 201, headers: { 'Content-Type': 'application/json' } }) },
    'GET /v1/owner/session': () => (created ? session : apiError(401, 'not_authenticated', 'Sign in to continue.')),
  })
  await fillForm()
  await fireEvent.click(screen.getByRole('button', { name: 'Create owner' }))
  expect(await screen.findByRole('heading', { name: 'Devices' })).toBeTruthy()
  const setupCall = calls.find((call) => call.method === 'POST' && call.path === '/v1/owner/setup')!
  expect(setupCall.body).toEqual({ token: TOKEN, name: 'Alex Rivera', password: PASSWORD, code: '123456' })
  expect(window.location.pathname).toBe('/')
  expect(screen.queryByText(SECRET)).toBeNull()
  expect(storedText()).not.toContain(PASSWORD)
})

test('shows the server message, clears the code and stays on the form when the code is wrong', async () => {
  openApp(`/setup#token=${TOKEN}`, {
    'GET /config.json': () => ({ ...config, setupRequired: true }),
    'POST /v1/owner/setup/details': () => details,
    'POST /v1/owner/setup': () => apiError(400, 'invalid_setup', 'The code is incorrect or was already used. Wait for the next code and try again.'),
  })
  await fillForm('000000')
  await fireEvent.click(screen.getByRole('button', { name: 'Create owner' }))
  expect((await screen.findByRole('alert')).textContent).toContain('The code is incorrect or was already used')
  expect((screen.getByLabelText('Six-digit code') as HTMLInputElement).value).toBe('')
  expect(screen.getByRole('heading', { name: 'Create the first owner' })).toBeTruthy()
})

test('explains an expired or reused setup link and how to get a new one', async () => {
  openApp(`/setup#token=${TOKEN}`, {
    'GET /config.json': () => ({ ...config, setupRequired: true }),
    'POST /v1/owner/setup/details': () => apiError(400, 'setup_token_invalid', 'This setup link is invalid or has expired.'),
  })
  expect(await screen.findByRole('heading', { name: 'This setup link does not work' })).toBeTruthy()
  expect(screen.getByRole('alert').textContent).toBe('This setup link is invalid or has expired.')
  expect(screen.getByText('sesame-server owner reset NAME')).toBeTruthy()
  expect(screen.queryByRole('button', { name: 'Create owner' })).toBeNull()
})

test('asks for the setup link when the page opens without a token', async () => {
  const { calls } = openApp('/setup', { 'GET /config.json': () => ({ ...config, setupRequired: true }) })
  expect(await screen.findByRole('heading', { name: 'Open your setup link' })).toBeTruthy()
  expect(calls.some((call) => call.path === '/v1/owner/setup/details')).toBe(false)
})

test('picks up a token pasted into the same tab after the page loaded', async () => {
  openApp('/setup', { 'GET /config.json': () => ({ ...config, setupRequired: true }), 'POST /v1/owner/setup/details': () => details })
  await screen.findByRole('heading', { name: 'Open your setup link' })
  window.location.hash = `token=${TOKEN}`
  expect(await screen.findByRole('heading', { name: 'Create the first owner' })).toBeTruthy()
  await waitFor(() => expect(window.location.hash).toBe(''))
})

test('locks the name for an invited owner', async () => {
  openApp(`/setup#token=${TOKEN}`, {
    'GET /config.json': () => ({ ...config, setupRequired: false }),
    'POST /v1/owner/setup/details': () => ({ ...details, ownerName: 'Sam Okafor', firstOwner: false }),
  })
  const name = (await screen.findByLabelText('Name')) as HTMLInputElement
  expect(name.value).toBe('Sam Okafor')
  expect(name.readOnly).toBe(true)
  expect(screen.queryByRole('checkbox')).toBeNull()
  expect(screen.getByRole('heading', { name: 'Finish your owner sign-in' })).toBeTruthy()
})

const setupRoutes = (created: () => void) => ({
  'GET /config.json': () => ({ ...config, setupRequired: true }),
  'POST /v1/owner/setup/details': () => details,
  'POST /v1/owner/setup': () => { created(); return new Response(JSON.stringify(session), { status: 201, headers: { 'Content-Type': 'application/json' } }) },
  'GET /v1/owner/session': () => apiError(401, 'not_authenticated', 'Sign in to continue.'),
})

test('offers the update check unchecked and leaves it out of the request until chosen', async () => {
  const { calls } = openApp(`/setup#token=${TOKEN}`, setupRoutes(() => undefined))
  const box = (await screen.findByRole('checkbox', { name: /Check for updates/ })) as HTMLInputElement
  expect(box.checked).toBe(false)
  expect(screen.getByText("Only this server's address is visible to Sesame.")).toBeTruthy()
  await fillForm()
  await fireEvent.click(screen.getByRole('button', { name: 'Create owner' }))
  await waitFor(() => expect(calls.some((call) => call.method === 'POST' && call.path === '/v1/owner/setup')).toBe(true))
  expect(calls.find((call) => call.path === '/v1/owner/setup')!.body).not.toHaveProperty('updateChecks')
})

test('sends updateChecks when the box is ticked', async () => {
  const { calls } = openApp(`/setup#token=${TOKEN}`, setupRoutes(() => undefined))
  await fillForm()
  await fireEvent.click(await screen.findByRole('checkbox', { name: /Check for updates/ }))
  await fireEvent.click(screen.getByRole('button', { name: 'Create owner' }))
  await waitFor(() => expect(calls.some((call) => call.method === 'POST' && call.path === '/v1/owner/setup')).toBe(true))
  expect(calls.find((call) => call.path === '/v1/owner/setup')!.body).toEqual({ token: TOKEN, name: 'Alex Rivera', password: PASSWORD, code: '123456', updateChecks: true })
})
