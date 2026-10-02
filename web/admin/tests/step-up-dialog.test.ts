import { cleanup, fireEvent, render, screen, within } from '@testing-library/svelte'
import { afterEach, expect, test, vi } from 'vitest'
import type { AdminAccount } from '../src/lib/types'
import App from '../src/App.svelte'

const fetchMock = vi.fn()
vi.stubGlobal('fetch', fetchMock)

const admin: AdminAccount = {
  id: 'ops',
  email: 'ops@example.invalid',
  role: 'ops',
  mfaVerified: true,
  suspended: false,
  createdAt: '2026-08-31T00:00:00Z',
  permissions: ['flags:manage'],
}

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

type CallLog = {
  flagUpdates: number
  stepUps: number
  stepUpBodies: string[]
}

function mockApi(state: { flagFailures: number; stepUpFailures: number }): CallLog {
  const log: CallLog = { flagUpdates: 0, stepUps: 0, stepUpBodies: [] }
  fetchMock.mockImplementation(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    const method = (init?.method || 'GET').toUpperCase()
    if (url.endsWith('/v1/admin/auth/me')) return jsonResponse({ admin, deploymentProfile: 'operator' })
    if (url.endsWith('/v1/admin/overview')) return jsonResponse({ overview: {} })
    if (url.endsWith('/v1/admin/flags')) return jsonResponse({ flags: [{ key: 'registration_mode', value: 'invite', updatedAt: '2026-08-31T00:00:00Z' }] })
    if (url.endsWith('/v1/admin/auth/csrf')) return jsonResponse({ token: 'csrf-token' })
    if (url.endsWith('/v1/admin/auth/step-up') && method === 'POST') {
      log.stepUps += 1
      log.stepUpBodies.push(String(init?.body ?? ''))
      if (log.stepUps <= state.stepUpFailures) {
        return jsonResponse({ error: { code: 'invalid_admin_credentials', message: 'The admin password or MFA code is incorrect.' } }, 401)
      }
      return jsonResponse({ stepUpExpiresAt: '2026-09-29T12:00:00Z', windowSeconds: 300 })
    }
    if (url.endsWith('/v1/admin/flags/registration_mode') && method === 'PATCH') {
      log.flagUpdates += 1
      if (log.flagUpdates <= state.flagFailures) {
        return jsonResponse({ error: { code: 'admin_step_up_required', message: 'Re-enter your admin password or MFA code to continue.' } }, 403)
      }
      return new Response(null, { status: 204 })
    }
    return jsonResponse({ error: { code: 'not_found', message: 'not found' } }, 404)
  })
  return log
}

async function triggerFlagChange() {
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: 'Feature flags' }))
  await fireEvent.change(await screen.findByRole('combobox'), { target: { value: 'public' } })
  return screen.findByRole('dialog')
}

afterEach(() => {
  cleanup()
  fetchMock.mockReset()
})

test('requests a fresh credential and retries the action after re-authentication', async () => {
  const log = mockApi({ flagFailures: 1, stepUpFailures: 0 })
  const dialog = await triggerFlagChange()
  expect(within(dialog).getByText('Re-authenticate to continue')).toBeTruthy()
  expect(log.flagUpdates).toBe(1)

  await fireEvent.input(within(dialog).getByLabelText('Admin password'), { target: { value: 'fictional-admin-password-123' } })
  await fireEvent.click(within(dialog).getByRole('button', { name: 'Confirm' }))

  expect(await screen.findByText('Feature flag updated live.')).toBeTruthy()
  expect(log.flagUpdates).toBe(2)
  expect(log.stepUps).toBe(1)
  expect(log.stepUpBodies[0]).toBe(JSON.stringify({ password: 'fictional-admin-password-123' }))
  expect(screen.queryByRole('dialog')).toBeNull()
})

test('leaves the action unperformed when the dialog is cancelled', async () => {
  const log = mockApi({ flagFailures: 100, stepUpFailures: 0 })
  const dialog = await triggerFlagChange()
  await fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }))

  expect(await screen.findByText('The action was cancelled.')).toBeTruthy()
  expect(screen.queryByRole('dialog')).toBeNull()
  expect(log.flagUpdates).toBe(1)
  expect(log.stepUps).toBe(0)
})

test('keeps the dialog open with an error after a failed re-authentication', async () => {
  const log = mockApi({ flagFailures: 1, stepUpFailures: 1 })
  const dialog = await triggerFlagChange()
  await fireEvent.input(within(dialog).getByLabelText('Admin password'), { target: { value: 'fictional-wrong-password' } })
  await fireEvent.click(within(dialog).getByRole('button', { name: 'Confirm' }))

  expect((await within(dialog).findByRole('alert')).textContent).toBe('The admin password or MFA code is incorrect.')
  expect(screen.getByRole('dialog')).toBeTruthy()
  expect(log.flagUpdates).toBe(1)

  await fireEvent.input(within(dialog).getByLabelText('Admin password'), { target: { value: '' } })
  await fireEvent.input(within(dialog).getByLabelText('Six-digit code'), { target: { value: '123456' } })
  await fireEvent.click(within(dialog).getByRole('button', { name: 'Confirm' }))

  expect(await screen.findByText('Feature flag updated live.')).toBeTruthy()
  expect(log.flagUpdates).toBe(2)
  expect(log.stepUps).toBe(2)
  expect(log.stepUpBodies[1]).toBe(JSON.stringify({ code: '123456' }))
  expect(screen.queryByRole('dialog')).toBeNull()
})
