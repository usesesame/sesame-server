import { cleanup, fireEvent, render, screen } from '@testing-library/svelte'
import { afterEach, expect, test, vi } from 'vitest'
import type { AdminAccount, Flag } from '../src/lib/types'

const api = vi.hoisted(() => ({ request: vi.fn(), mutate: vi.fn() }))

vi.mock('../src/lib/api', () => {
  class APIError extends Error {
    constructor(message: string, public status: number, public code = '') { super(message) }
  }
  return { APIError, apiURL: 'https://api.test.invalid', request: api.request, mutate: api.mutate, onStepUpRequired: vi.fn() }
})

import App from '../src/App.svelte'

function admin(overrides: Partial<AdminAccount> = {}): AdminAccount {
  return {
    id: 'ops',
    email: 'ops@example.invalid',
    role: 'super',
    mfaVerified: true,
    suspended: false,
    createdAt: '2026-01-01T00:00:00Z',
    permissions: ['flags:manage'],
    ...overrides,
  }
}

function flags(): Flag[] {
  return [
    { key: 'registration_mode', value: 'invite', updatedAt: '2026-08-31T10:00:00Z' },
    { key: 'beta_downloads', value: 'false', updatedAt: '2026-08-31T10:00:00Z' },
  ]
}

function mockApp(routes: (path: string) => unknown, overrides: Partial<AdminAccount> = {}) {
  api.request.mockImplementation(async (path: string) => {
    if (path === '/v1/admin/auth/me') return { admin: admin(overrides) }
    if (path === '/v1/admin/overview') return { overview: {} }
    return routes(path)
  })
}

afterEach(() => {
  cleanup()
  api.request.mockReset()
  api.mutate.mockReset()
})

test('loads runtime feature flags with their current values', async () => {
  mockApp((path) => path === '/v1/admin/flags' ? { flags: flags() } : {})
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: 'Feature flags' }))
  expect(await screen.findByText('Registration')).toBeTruthy()
  expect(screen.getByText('Decides who can create a website account.')).toBeTruthy()
  expect(api.request).toHaveBeenCalledWith('/v1/admin/flags')
  expect(screen.getByText('Beta downloads')).toBeTruthy()
  expect((screen.getByDisplayValue('Invitation only') as HTMLSelectElement).value).toBe('invite')
  expect((screen.getByDisplayValue('Off') as HTMLSelectElement).value).toBe('false')
})

test('stages a registration change and sends it only after confirmation', async () => {
  mockApp((path) => path === '/v1/admin/flags' ? { flags: flags() } : {})
  api.mutate.mockResolvedValue({})
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: 'Feature flags' }))
  await fireEvent.change(await screen.findByDisplayValue('Invitation only'), { target: { value: 'public' } })
  expect(api.mutate).not.toHaveBeenCalled()
  expect(screen.getByText('Change registration from Invitation only to Public? Anyone can create a website account. The change applies at once and is written to the audit log.')).toBeTruthy()
  await fireEvent.click(screen.getByRole('button', { name: 'Apply change' }))
  expect(api.mutate).toHaveBeenCalledTimes(1)
  expect(api.mutate).toHaveBeenCalledWith('/v1/admin/flags/registration_mode', 'PATCH', { value: 'public' })
  expect(await screen.findByText('Feature flag updated live.')).toBeTruthy()
  expect(api.request).toHaveBeenCalledTimes(4)
})

test('cancelling a staged registration change sends nothing and restores the value', async () => {
  mockApp((path) => path === '/v1/admin/flags' ? { flags: flags() } : {})
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: 'Feature flags' }))
  const select = await screen.findByDisplayValue('Invitation only') as HTMLSelectElement
  await fireEvent.change(select, { target: { value: 'closed' } })
  expect(screen.getByText(/No one can create a website account\./)).toBeTruthy()
  await fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
  expect(screen.queryByRole('button', { name: 'Apply change' })).toBeNull()
  expect(select.value).toBe('invite')
  expect(api.mutate).not.toHaveBeenCalled()
})

test('restores the registration value when the confirmed change fails', async () => {
  mockApp((path) => path === '/v1/admin/flags' ? { flags: flags() } : {})
  api.mutate.mockRejectedValue(new Error('The change was refused.'))
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: 'Feature flags' }))
  const select = await screen.findByDisplayValue('Invitation only') as HTMLSelectElement
  await fireEvent.change(select, { target: { value: 'public' } })
  await fireEvent.click(screen.getByRole('button', { name: 'Apply change' }))
  expect(await screen.findByText('The change was refused.')).toBeTruthy()
  expect(select.value).toBe('invite')
  expect(screen.queryByRole('button', { name: 'Apply change' })).toBeNull()
})

test('toggles a boolean flag', async () => {
  mockApp((path) => path === '/v1/admin/flags' ? { flags: flags() } : {}, { role: 'ops', permissions: [] })
  api.mutate.mockResolvedValue({})
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: 'Feature flags' }))
  await fireEvent.change(await screen.findByDisplayValue('Off'), { target: { value: 'true' } })
  expect(api.mutate).toHaveBeenCalledWith('/v1/admin/flags/beta_downloads', 'PATCH', { value: 'true' })
})

test('readonly staff cannot change flags', async () => {
  mockApp((path) => path === '/v1/admin/flags' ? { flags: flags() } : {}, { role: 'readonly', permissions: [] })
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: 'Feature flags' }))
  expect((await screen.findByDisplayValue('Invitation only') as HTMLSelectElement).disabled).toBe(true)
  expect((screen.getByDisplayValue('Off') as HTMLSelectElement).disabled).toBe(true)
})

test('locks a flag while its update is pending and restores it after a failure', async () => {
  mockApp((path) => path === '/v1/admin/flags' ? { flags: flags() } : {})
  let reject: (reason: Error) => void = () => {}
  api.mutate.mockReturnValue(new Promise((_, fail) => { reject = fail }))
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: 'Feature flags' }))
  const select = await screen.findByDisplayValue('Off') as HTMLSelectElement
  await fireEvent.change(select, { target: { value: 'true' } })
  expect(select.disabled).toBe(true)
  reject(new Error('The change was refused.'))
  expect(await screen.findByText('The change was refused.')).toBeTruthy()
  expect(select.disabled).toBe(false)
  expect(select.value).toBe('false')
})
