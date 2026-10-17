import { fireEvent, screen, waitFor, within } from '@testing-library/svelte'
import { expect, test } from 'vitest'
import { formatDate } from '../src/lib/format'
import { apiError, json } from './support/fake-api'
import { openApp, useCleanApp } from './support/app'

useCleanApp()

const PASSWORD = 'fictional password for tests'
const updatedAt = '2026-10-09T09:30:00Z'
const stepUpNeeded = () => apiError(403, 'step_up_required', 'Confirm your password and a current code to continue.')
const aliveHolders = [
  { kind: 'owner', id: 'own-1', name: 'Alex Rivera', hasVault: true, devices: 2, currentRevision: 14, storedBytes: 1536, updatedAt },
  { kind: 'member', id: 'mem-1', name: 'Priya Raman', hasVault: false, devices: 0, currentRevision: 0, storedBytes: 0, updatedAt: null },
]
const resetHolder = { ...aliveHolders[0], hasVault: false, currentRevision: 0, storedBytes: 0, updatedAt: null }

async function openSync() {
  await fireEvent.click(await screen.findByRole('link', { name: 'Sync' }))
}

async function confirmStepUp(dialog: HTMLElement) {
  await fireEvent.input(within(dialog).getByLabelText('Password'), { target: { value: PASSWORD } })
  await fireEvent.input(within(dialog).getByLabelText(/^Six-digit code/), { target: { value: '123456' } })
  await fireEvent.click(within(dialog).getByRole('button', { name: 'Confirm' }))
}

test('says when this server build does not include Sync and shows nothing else', async () => {
  openApp('/', { 'GET /v1/owner/sync': () => ({ available: false, enabled: false, holders: [] }) })
  await openSync()
  expect(await screen.findByText('This server build does not include Sync.')).toBeTruthy()
  expect(screen.queryByText('Sync is off')).toBeNull()
  expect(screen.queryByText(/Sync has not had an independent security review/)).toBeNull()
})

test('shows the switch off and says devices cannot sync until it is on', async () => {
  openApp('/', { 'GET /v1/owner/sync': () => ({ available: true, enabled: false, holders: [] }) })
  await openSync()
  expect(await screen.findByText('Sync is off')).toBeTruthy()
  expect(screen.getByText('Devices cannot sync until Sync is turned on.')).toBeTruthy()
  expect(screen.getByRole('button', { name: 'Turn on Sync' })).toBeTruthy()
})

test('shows the switch on and one row for each person', async () => {
  openApp('/', { 'GET /v1/owner/sync': () => ({ available: true, enabled: true, holders: aliveHolders }) })
  await openSync()
  expect(await screen.findByText('Sync is on')).toBeTruthy()
  expect(screen.getByRole('button', { name: 'Turn off Sync' })).toBeTruthy()
  expect(screen.queryByText('Devices cannot sync until Sync is turned on.')).toBeNull()
  expect(screen.getByText('Alex Rivera', { selector: '.row-main strong' })).toBeTruthy()
  expect(screen.getByText('Priya Raman', { selector: '.row-main strong' })).toBeTruthy()
  expect(screen.getAllByRole('button', { name: 'Reset synced vault' })).toHaveLength(1)
})

test('states the synced facts for a person with a vault and without one', async () => {
  openApp('/', { 'GET /v1/owner/sync': () => ({ available: true, enabled: true, holders: aliveHolders }) })
  await openSync()
  expect(await screen.findByText(`2 devices, version 14, uses 1.5 KB, last changed ${formatDate(updatedAt)}`)).toBeTruthy()
  expect(screen.getByText('no synced vault yet')).toBeTruthy()
})

test('explains what a reset does before it runs', async () => {
  openApp('/', { 'GET /v1/owner/sync': () => ({ available: true, enabled: true, holders: aliveHolders }) })
  await openSync()
  await fireEvent.click(await screen.findByRole('button', { name: 'Reset synced vault' }))
  expect(screen.getByText("This deletes the stored encrypted copy and history of Alex Rivera's synced vault. Their devices keep their local vaults, and they can start Sync again.")).toBeTruthy()
  expect(screen.getByRole('button', { name: 'Reset synced vault' })).toBeTruthy()
  expect(screen.getByRole('button', { name: 'Cancel' })).toBeTruthy()
})

test('cancelling the reset calls nothing', async () => {
  const { calls } = openApp('/', { 'GET /v1/owner/sync': () => ({ available: true, enabled: true, holders: aliveHolders }) })
  await openSync()
  await fireEvent.click(await screen.findByRole('button', { name: 'Reset synced vault' }))
  await fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
  expect(screen.getByRole('button', { name: 'Reset synced vault' })).toBeTruthy()
  expect(calls.some((call) => call.method === 'DELETE')).toBe(false)
  expect(calls.filter((call) => call.method === 'GET' && call.path === '/v1/owner/sync')).toHaveLength(1)
})

test('confirming the reset calls the delete route once and reloads the list', async () => {
  let holders = aliveHolders
  const { calls } = openApp('/', {
    'GET /v1/owner/sync': () => ({ available: true, enabled: true, holders }),
    'DELETE /v1/owner/sync/owner/own-1': () => { holders = [resetHolder, aliveHolders[1]]; return undefined },
  })
  await openSync()
  await fireEvent.click(await screen.findByRole('button', { name: 'Reset synced vault' }))
  await fireEvent.click(screen.getByRole('button', { name: 'Reset synced vault' }))
  expect(await screen.findByText("Alex Rivera's synced vault was reset.")).toBeTruthy()
  await waitFor(() => expect(screen.getAllByText('no synced vault yet')).toHaveLength(2))
  expect(calls.filter((call) => call.method === 'DELETE')).toHaveLength(1)
  expect(calls.filter((call) => call.method === 'GET' && call.path === '/v1/owner/sync')).toHaveLength(2)
  expect(calls.some((call) => call.path === '/v1/owner/step-up')).toBe(false)
})

test('resetting a vault asks for a recent sign-in and retries once', async () => {
  let attempts = 0
  let holders = aliveHolders
  const { calls } = openApp('/', {
    'GET /v1/owner/sync': () => ({ available: true, enabled: true, holders }),
    'DELETE /v1/owner/sync/owner/own-1': () => {
      if (++attempts === 1) return stepUpNeeded()
      holders = [resetHolder, aliveHolders[1]]
      return undefined
    },
    'POST /v1/owner/step-up': () => json({ ok: true }),
  })
  await openSync()
  await fireEvent.click(await screen.findByRole('button', { name: 'Reset synced vault' }))
  await fireEvent.click(screen.getByRole('button', { name: 'Reset synced vault' }))
  const dialog = await screen.findByRole('dialog')
  await confirmStepUp(dialog)
  expect(await screen.findByText("Alex Rivera's synced vault was reset.")).toBeTruthy()
  expect(screen.queryByRole('dialog')).toBeNull()
  expect(calls.filter((call) => call.method === 'DELETE')).toHaveLength(2)
  expect(calls.find((call) => call.path === '/v1/owner/step-up')!.body).toEqual({ password: PASSWORD, code: '123456' })
})

test('turning Sync on goes through the step-up dialog and retries once', async () => {
  let enabled = false
  let attempts = 0
  const { calls } = openApp('/', {
    'GET /v1/owner/sync': () => ({ available: true, enabled, holders: aliveHolders }),
    'PATCH /v1/owner/flags/sync_enabled': () => {
      if (++attempts === 1) return stepUpNeeded()
      enabled = true
      return json({ key: 'sync_enabled', description: 'Sync between devices', enabled: true, updatedAt })
    },
    'POST /v1/owner/step-up': () => json({ ok: true }),
  })
  await openSync()
  await fireEvent.click(await screen.findByRole('button', { name: 'Turn on Sync' }))
  const dialog = await screen.findByRole('dialog')
  expect(within(dialog).getByText('Confirm it is you')).toBeTruthy()
  expect(calls.filter((call) => call.method === 'PATCH')).toHaveLength(1)
  await confirmStepUp(dialog)
  expect(await screen.findByText('Sync was turned on.')).toBeTruthy()
  expect(screen.queryByRole('dialog')).toBeNull()
  expect(await screen.findByText('Sync is on')).toBeTruthy()
  const patches = calls.filter((call) => call.method === 'PATCH')
  expect(patches).toHaveLength(2)
  expect(patches[0].body).toEqual({ enabled: true })
  expect(calls.find((call) => call.path === '/v1/owner/step-up')!.body).toEqual({ password: PASSWORD, code: '123456' })
})
