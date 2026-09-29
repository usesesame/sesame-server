import { cleanup, fireEvent, render, screen, within } from '@testing-library/svelte'
import { afterEach, expect, test, vi } from 'vitest'
import type { AdminAccount } from '../src/lib/types'

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
    lastLoginAt: '2026-08-31T09:00:00Z',
    permissions: ['admins:manage', 'users:read', 'users:manage', 'users:delete'],
    ...overrides,
  }
}

const other = admin({ id: 'admin-support', email: 'support@example.invalid', role: 'support', mfaVerified: false, lastLoginAt: undefined, permissions: ['support:read'] })

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
  vi.unstubAllGlobals()
})

test('lists administrators with setup and sign-in state', async () => {
  mockApp((path) => path === '/v1/admin/admins' ? { admins: [admin(), other] } : {})
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: 'Administrators' }))
  expect(await screen.findByText('support@example.invalid')).toBeTruthy()
  expect(api.request).toHaveBeenCalledWith('/v1/admin/admins')
  expect(screen.getByText(/MFA verified/)).toBeTruthy()
  expect(screen.getByText(/Setup pending/)).toBeTruthy()
  expect(screen.getByRole('heading', { name: 'Invite an administrator' })).toBeTruthy()
})

test('creates a one-time setup link for an invitation', async () => {
  mockApp((path) => path === '/v1/admin/admins' ? { admins: [admin()] } : {})
  api.mutate.mockResolvedValue({ setupUrl: 'https://admin.test.invalid/setup?token=one-time' })
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: 'Administrators' }))
  await fireEvent.input(await screen.findByPlaceholderText('name@example.com'), { target: { value: 'new@example.invalid' } })
  await fireEvent.change(screen.getAllByRole('combobox')[0], { target: { value: 'billing' } })
  await fireEvent.click(screen.getByRole('button', { name: 'Create setup link' }))
  expect(api.mutate).toHaveBeenCalledWith('/v1/admin/admins', 'POST', { email: 'new@example.invalid', role: 'billing' })
  expect(await screen.findByDisplayValue('https://admin.test.invalid/setup?token=one-time')).toBeTruthy()
  expect(await screen.findByText('Administrator invited. Share the one-time link through a trusted channel.')).toBeTruthy()
})

test('changes an administrator role', async () => {
  mockApp((path) => path === '/v1/admin/admins' ? { admins: [admin(), other] } : {})
  api.mutate.mockResolvedValue({})
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: 'Administrators' }))
  const row = (await screen.findByText('support@example.invalid')).closest('.setting-row') as HTMLElement
  await fireEvent.change(within(row).getByDisplayValue('support'), { target: { value: 'ops' } })
  expect(api.mutate).toHaveBeenCalledWith('/v1/admin/admins/admin-support', 'PATCH', { role: 'ops', suspended: false })
  expect(await screen.findByText('Administrator updated.')).toBeTruthy()
})

test('suspends and unsuspends another administrator', async () => {
  let suspended = false
  mockApp((path) => path === '/v1/admin/admins' ? { admins: [admin(), admin({ ...other, suspended })] } : {})
  api.mutate.mockImplementation(async (path: string, method: string, body: unknown) => {
    if (path === '/v1/admin/admins/admin-support' && method === 'PATCH') suspended = (body as { suspended: boolean }).suspended
    return {}
  })
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: 'Administrators' }))
  const row = (await screen.findByText('support@example.invalid')).closest('.setting-row') as HTMLElement
  await fireEvent.click(within(row).getByRole('button', { name: 'Suspend' }))
  expect(api.mutate).toHaveBeenCalledWith('/v1/admin/admins/admin-support', 'PATCH', { role: 'support', suspended: true })
  const suspendedRow = (await screen.findByText('support@example.invalid')).closest('.setting-row') as HTMLElement
  await fireEvent.click(within(suspendedRow).getByRole('button', { name: 'Unsuspend' }))
  expect(api.mutate).toHaveBeenLastCalledWith('/v1/admin/admins/admin-support', 'PATCH', { role: 'support', suspended: false })
})

test('disables self-service suspension and deletion', async () => {
  mockApp((path) => path === '/v1/admin/admins' ? { admins: [admin(), other] } : {})
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: 'Administrators' }))
  const row = screen.getByText('ops@example.invalid', { selector: '.setting-row strong' }).closest('.setting-row') as HTMLElement
  expect((within(row).getByRole('button', { name: 'Suspend' }) as HTMLButtonElement).disabled).toBe(true)
  expect((within(row).getByRole('button', { name: 'Delete' }) as HTMLButtonElement).disabled).toBe(true)
})

test('deletes an administrator only after confirmation', async () => {
  const confirmMock = vi.fn(() => true)
  vi.stubGlobal('confirm', confirmMock)
  mockApp((path) => path === '/v1/admin/admins' ? { admins: [admin(), other] } : {})
  api.mutate.mockResolvedValue({})
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: 'Administrators' }))
  const row = (await screen.findByText('support@example.invalid')).closest('.setting-row') as HTMLElement
  await fireEvent.click(within(row).getByRole('button', { name: 'Delete' }))
  expect(confirmMock).toHaveBeenCalledWith('Delete the administrator support@example.invalid? Their audit entries will remain.')
  expect(api.mutate).toHaveBeenCalledWith('/v1/admin/admins/admin-support', 'DELETE')
  expect(await screen.findByText('Administrator deleted.')).toBeTruthy()
})

test('readonly staff see administrators without controls', async () => {
  mockApp((path) => path === '/v1/admin/admins' ? { admins: [admin(), other] } : {}, { role: 'readonly', permissions: ['users:read'] })
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: 'Administrators' }))
  expect(await screen.findByText('support@example.invalid')).toBeTruthy()
  expect(screen.queryByRole('heading', { name: 'Invite an administrator' })).toBeNull()
  expect(screen.queryByRole('button', { name: 'Suspend' })).toBeNull()
  expect(screen.queryByRole('button', { name: 'Delete' })).toBeNull()
  expect((screen.getByDisplayValue('support') as HTMLSelectElement).disabled).toBe(true)
})
