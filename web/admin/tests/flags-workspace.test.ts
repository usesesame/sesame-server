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
  expect(await screen.findByText('registration mode')).toBeTruthy()
  expect(api.request).toHaveBeenCalledWith('/v1/admin/flags')
  expect(screen.getByText('beta downloads')).toBeTruthy()
  expect((screen.getByDisplayValue('Invitation only') as HTMLSelectElement).value).toBe('invite')
  expect((screen.getByDisplayValue('Off') as HTMLSelectElement).value).toBe('false')
})

test('updates the registration mode', async () => {
  mockApp((path) => path === '/v1/admin/flags' ? { flags: flags() } : {})
  api.mutate.mockResolvedValue({})
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: 'Feature flags' }))
  await fireEvent.change(await screen.findByDisplayValue('Invitation only'), { target: { value: 'public' } })
  expect(api.mutate).toHaveBeenCalledWith('/v1/admin/flags/registration_mode', 'PATCH', { value: 'public' })
  expect(await screen.findByText('Feature flag updated live.')).toBeTruthy()
  expect(api.request).toHaveBeenCalledTimes(4)
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
