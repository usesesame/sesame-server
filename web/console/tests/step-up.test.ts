import { fireEvent, screen, waitFor, within } from '@testing-library/svelte'
import { expect, test } from 'vitest'
import { apiError, json } from './support/fake-api'
import { openApp, useCleanApp } from './support/app'

useCleanApp()

const PASSWORD = 'fictional password for tests'
const invite = { owner: { id: 'own-9', name: 'Sam Okafor', createdAt: '2026-10-10T00:00:00Z', lastLoginAt: null, setupPending: true, current: false }, setupToken: 'tok', link: 'https://sesame.example.test/setup#token=tok', expiresAt: '2099-01-01T00:00:00Z' }
const stepUpNeeded = () => apiError(403, 'step_up_required', 'Confirm your password and a current code to continue.')

async function startInvite() {
  await fireEvent.click(await screen.findByRole('link', { name: 'Owners' }))
  await fireEvent.input(await screen.findByLabelText('Name'), { target: { value: 'Sam Okafor' } })
  await fireEvent.click(screen.getByRole('button', { name: 'Invite owner' }))
  return screen.findByRole('dialog')
}

async function confirm(dialog: HTMLElement, code = '123456', password = PASSWORD) {
  await fireEvent.input(within(dialog).getByLabelText('Password'), { target: { value: password } })
  await fireEvent.input(within(dialog).getByLabelText(/^Six-digit code/), { target: { value: code } })
  await fireEvent.click(within(dialog).getByRole('button', { name: 'Confirm' }))
}

test('opens the step-up prompt on step_up_required, confirms, and repeats the action once', async () => {
  let attempts = 0
  const { calls } = openApp('/', {
    'POST /v1/owner/owners': () => (++attempts === 1 ? stepUpNeeded() : json(invite, 201)),
    'POST /v1/owner/step-up': () => json({ ok: true }),
  })
  const dialog = await startInvite()
  expect(within(dialog).getByText('Confirm it is you')).toBeTruthy()
  expect((within(dialog).getByRole('button', { name: 'Confirm' }) as HTMLButtonElement).disabled).toBe(true)
  await confirm(dialog)
  expect(await screen.findByText('Setup link for Sam Okafor')).toBeTruthy()
  expect(screen.queryByRole('dialog')).toBeNull()
  expect(calls.find((call) => call.path === '/v1/owner/step-up')!.body).toEqual({ password: PASSWORD, code: '123456' })
  expect(calls.filter((call) => call.method === 'POST' && call.path === '/v1/owner/owners')).toHaveLength(2)
})

test('keeps the prompt open with the server message after a wrong password or code', async () => {
  let stepUps = 0
  let attempts = 0
  const { calls } = openApp('/', {
    'POST /v1/owner/owners': () => (++attempts === 1 ? stepUpNeeded() : json(invite, 201)),
    'POST /v1/owner/step-up': () => (++stepUps === 1 ? apiError(401, 'invalid_credentials', 'The password or code is incorrect.') : json({ ok: true })),
  })
  const dialog = await startInvite()
  await confirm(dialog, '000000', 'wrong password here')
  expect((await within(dialog).findByRole('alert')).textContent).toBe('The password or code is incorrect.')
  expect(screen.getByRole('dialog')).toBeTruthy()
  expect(calls.filter((call) => call.path === '/v1/owner/owners' && call.method === 'POST')).toHaveLength(1)
  await confirm(dialog)
  expect(await screen.findByText('Setup link for Sam Okafor')).toBeTruthy()
  expect(stepUps).toBe(2)
})

test('leaves the action undone when the prompt is cancelled', async () => {
  const { calls } = openApp('/', { 'POST /v1/owner/owners': () => stepUpNeeded() })
  const dialog = await startInvite()
  await fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }))
  expect(await screen.findByText('The action was cancelled.')).toBeTruthy()
  expect(screen.queryByRole('dialog')).toBeNull()
  expect(calls.filter((call) => call.path === '/v1/owner/owners' && call.method === 'POST')).toHaveLength(1)
  expect(calls.some((call) => call.path === '/v1/owner/step-up')).toBe(false)
})

test('closes the prompt on Escape', async () => {
  openApp('/', { 'POST /v1/owner/owners': () => stepUpNeeded() })
  const dialog = await startInvite()
  await fireEvent.keyDown(within(dialog).getByLabelText('Password'), { key: 'Escape' })
  expect(await screen.findByText('The action was cancelled.')).toBeTruthy()
})

test('clears the password and code fields after the prompt closes', async () => {
  let attempts = 0
  openApp('/', {
    'POST /v1/owner/owners': () => (++attempts === 1 ? stepUpNeeded() : json(invite, 201)),
    'POST /v1/owner/step-up': () => json({ ok: true }),
  })
  let dialog = await startInvite()
  await confirm(dialog)
  await screen.findByText('Setup link for Sam Okafor')
  attempts = 0
  await fireEvent.input(screen.getByLabelText('Name'), { target: { value: 'Jo Tanaka' } })
  await waitFor(() => expect((screen.getByRole('button', { name: 'Invite owner' }) as HTMLButtonElement).disabled).toBe(false))
  await fireEvent.click(screen.getByRole('button', { name: 'Invite owner' }))
  dialog = await screen.findByRole('dialog')
  expect((within(dialog).getByLabelText('Password') as HTMLInputElement).value).toBe('')
  expect((within(dialog).getByLabelText(/^Six-digit code/) as HTMLInputElement).value).toBe('')
})
