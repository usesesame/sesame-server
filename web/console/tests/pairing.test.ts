import { fireEvent, screen, waitFor } from '@testing-library/svelte'
import { expect, test, vi } from 'vitest'
import { apiError, json, member } from './support/fake-api'
import { openApp, storedText, useCleanApp } from './support/app'

useCleanApp()

const CODE = 'fictional-pairing-code-0123456789-abcdefghijklmnop'
const LINK = `https://sesame.example.test/pair#code=${CODE}&fp=ab12cd34`
const holder = { kind: 'owner', id: 'own-1', name: 'Alex Rivera' }

function issued(expiresAt = '2099-01-01T00:00:00Z', who = holder) {
  return json({ pairingId: 'pair-1', code: CODE, link: LINK, holder: who, expiresAt }, 201)
}

async function createCode() {
  await fireEvent.click(await screen.findByRole('link', { name: 'Pairing' }))
  const button = (await screen.findByRole('button', { name: 'Create pairing code' })) as HTMLButtonElement
  await waitFor(() => expect(button.disabled).toBe(false))
  await fireEvent.click(button)
}

test('creates a code for the owner and shows the link and code with copy buttons', async () => {
  const { calls } = openApp('/', { 'POST /v1/owner/pairings': () => issued() })
  const writeText = vi.fn(async () => undefined)
  Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
  await createCode()
  expect(await screen.findByText('Pairing code for Alex Rivera')).toBeTruthy()
  expect(screen.getByText(CODE)).toBeTruthy()
  expect(screen.getByText(LINK)).toBeTruthy()
  expect(calls.find((call) => call.method === 'POST' && call.path === '/v1/owner/pairings')!.body).toEqual({ self: true })
  const copies = screen.getAllByRole('button', { name: 'Copy' })
  await fireEvent.click(copies[0])
  expect(writeText).toHaveBeenCalledWith(LINK)
  await fireEvent.click(copies[1])
  expect(writeText).toHaveBeenLastCalledWith(CODE)
  expect(screen.getByText(/Expires in \d+:\d\d/)).toBeTruthy()
})

test('creates a code for a member and sends the device name hint', async () => {
  const { calls } = openApp('/', { 'POST /v1/owner/pairings': () => issued('2099-01-01T00:00:00Z', { kind: 'member', id: member.id, name: member.name }) })
  await fireEvent.click(await screen.findByRole('link', { name: 'Pairing' }))
  await screen.findByRole('option', { name: member.name })
  await waitFor(() => expect((screen.getByRole('button', { name: 'Create pairing code' }) as HTMLButtonElement).disabled).toBe(false))
  await fireEvent.change(screen.getByLabelText('Connect a device for'), { target: { value: member.id } })
  await fireEvent.input(screen.getByLabelText(/^Device name/), { target: { value: 'Priya laptop' } })
  await fireEvent.click(screen.getByRole('button', { name: 'Create pairing code' }))
  expect(await screen.findByText(`Pairing code for ${member.name}`)).toBeTruthy()
  expect(calls.find((call) => call.method === 'POST' && call.path === '/v1/owner/pairings')!.body).toEqual({ memberId: member.id, deviceName: 'Priya laptop' })
})

test('asks for step-up when the server requires it for another person', async () => {
  let attempts = 0
  openApp('/', {
    'POST /v1/owner/pairings': () => (++attempts === 1 ? apiError(403, 'step_up_required', 'Confirm your password and a current code to continue.') : issued()),
    'POST /v1/owner/step-up': () => json({ ok: true }),
  })
  await createCode()
  expect(await screen.findByRole('dialog')).toBeTruthy()
})

test('never puts the code or link in storage, the address bar or a request URL', async () => {
  const { calls } = openApp('/', { 'POST /v1/owner/pairings': () => issued() })
  await createCode()
  await screen.findByText(CODE)
  expect(storedText()).not.toContain(CODE)
  expect(window.location.href).not.toContain(CODE)
  expect(calls.every((call) => !call.path.includes(CODE))).toBe(true)
})

test('removes the code and link from the page when the owner navigates away', async () => {
  openApp('/', { 'POST /v1/owner/pairings': () => issued() })
  await createCode()
  await screen.findByText(CODE)
  await fireEvent.click(screen.getByRole('link', { name: 'Members' }))
  await screen.findByRole('heading', { name: 'Members' })
  expect(document.body.textContent).not.toContain(CODE)
  await fireEvent.click(screen.getByRole('link', { name: 'Pairing' }))
  await screen.findByRole('heading', { name: 'Pairing' })
  expect(document.body.textContent).not.toContain(CODE)
})

test('removes the code once the owner presses Done', async () => {
  openApp('/', { 'POST /v1/owner/pairings': () => issued() })
  await createCode()
  await screen.findByText(CODE)
  await fireEvent.click(screen.getByRole('button', { name: 'Done' }))
  expect(document.body.textContent).not.toContain(CODE)
})

test('cancels the code on the server and removes it from the page', async () => {
  let cancelled = false
  const { calls } = openApp('/', {
    'POST /v1/owner/pairings': () => issued(),
    'DELETE /v1/owner/pairings/pair-1': () => { cancelled = true; return undefined },
    'GET /v1/owner/pairings': () => ({ pairings: cancelled ? [] : [{ id: 'pair-1', holder, deviceName: '', createdBy: 'owner:own-1', createdAt: '2026-10-10T00:00:00Z', expiresAt: '2099-01-01T00:00:00Z' }] }),
  })
  await createCode()
  await screen.findByText(CODE)
  await fireEvent.click(screen.getByRole('button', { name: 'Cancel code' }))
  expect(await screen.findByText('The pairing code was cancelled.')).toBeTruthy()
  expect(document.body.textContent).not.toContain(CODE)
  expect(calls.some((call) => call.method === 'DELETE' && call.path === '/v1/owner/pairings/pair-1')).toBe(true)
  expect(await screen.findByText('No pending codes.')).toBeTruthy()
})

test('lists waiting codes without their secret and cancels one', async () => {
  const { calls } = openApp('/', {
    'GET /v1/owner/pairings': () => ({ pairings: [{ id: 'pair-7', holder: { kind: 'member', id: 'mem-1', name: 'Priya Raman' }, deviceName: 'Priya laptop', createdBy: 'owner:own-1', createdAt: '2026-10-10T00:00:00Z', expiresAt: '2099-01-01T00:00:00Z' }] }),
    'DELETE /v1/owner/pairings/pair-7': () => undefined,
  })
  await fireEvent.click(await screen.findByRole('link', { name: 'Pairing' }))
  expect(await screen.findByText(/Priya laptop/)).toBeTruthy()
  await fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
  await waitFor(() => expect(calls.some((call) => call.method === 'DELETE' && call.path === '/v1/owner/pairings/pair-7')).toBe(true))
})

test('hides the code when it expires', async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true, toFake: ['setInterval', 'clearInterval', 'Date'] })
  openApp('/', { 'POST /v1/owner/pairings': () => issued(new Date(Date.now() + 5_000).toISOString().replace(/\.\d+Z$/, 'Z')) })
  await createCode()
  await screen.findByText(CODE)
  await vi.advanceTimersByTimeAsync(10_000)
  await waitFor(() => expect(document.body.textContent).not.toContain(CODE))
  expect(screen.getByText('The pairing code expired.')).toBeTruthy()
})

test('shows the server message when the code cannot be created', async () => {
  openApp('/', { 'POST /v1/owner/pairings': () => apiError(503, 'unavailable', 'The server could not complete that action and no change was committed.') })
  await createCode()
  expect((await screen.findByRole('alert')).textContent).toContain('no change was committed')
  expect(screen.queryByText(CODE)).toBeNull()
})

test('shows a copy failure instead of claiming success', async () => {
  openApp('/', { 'POST /v1/owner/pairings': () => issued() })
  Object.defineProperty(navigator, 'clipboard', { value: { writeText: vi.fn(async () => { throw new Error('blocked') }) }, configurable: true })
  await createCode()
  await screen.findByText(CODE)
  await fireEvent.click(screen.getAllByRole('button', { name: 'Copy' })[1])
  expect((await screen.findByRole('alert')).textContent).toContain('Copying was blocked')
})
