import { cleanup, fireEvent, render, screen } from '@testing-library/svelte'
import { afterEach, expect, test, vi } from 'vitest'
import type { AdminAccount, User } from '../src/lib/types'

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
    role: 'support',
    mfaVerified: true,
    suspended: false,
    createdAt: '2026-01-01T00:00:00Z',
    permissions: ['users:read', 'users:manage'],
    ...overrides,
  }
}

function user(overrides: Partial<User> = {}): User {
  return {
    id: 'user-test',
    email: 'tester@example.invalid',
    emailVerified: true,
    betaAccess: false,
    createdAt: '2026-08-01T00:00:00Z',
    sessionCount: 1,
    deviceCount: 1,
    ...overrides,
  }
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
  vi.unstubAllGlobals()
})

test('loads the user list with account state', async () => {
  mockApp((path) => path.startsWith('/v1/admin/users?')
    ? { users: [user({ betaAccess: true })], total: 1 }
    : {})
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: 'Users' }))
  expect(await screen.findByText('tester@example.invalid')).toBeTruthy()
  expect(api.request).toHaveBeenCalledWith('/v1/admin/users?query=&size=100&page=1')
  expect(screen.getByText('Verified')).toBeTruthy()
  expect(screen.getByText('Eligible')).toBeTruthy()
  expect(screen.getByText('1 users')).toBeTruthy()
})

test('opens a user with sessions, devices, and a pending beta warning', async () => {
  const detail = user({
    emailVerified: false,
    betaAccess: true,
    sessions: [{ id: 'session-test', label: 'Firefox on Windows', createdAt: '2026-08-30T09:00:00Z', lastSeenAt: '2026-08-31T09:00:00Z', expiresAt: '2026-09-30T09:00:00Z' }],
    devices: [{ id: 'device-test', name: 'Office PC', connectedAt: '2026-08-01T00:00:00Z', expiresAt: '2026-09-01T00:00:00Z' }],
  })
  mockApp((path) => {
    if (path.startsWith('/v1/admin/users?')) return { users: [user()], total: 1 }
    if (path === '/v1/admin/users/user-test') return { user: detail }
    return {}
  })
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: 'Users' }))
  await fireEvent.click(await screen.findByText('tester@example.invalid'))
  expect(await screen.findByText('Firefox on Windows')).toBeTruthy()
  expect(api.request).toHaveBeenCalledWith('/v1/admin/users/user-test')
  expect(screen.getByText('Office PC')).toBeTruthy()
  expect(screen.getByText(/Beta is granted but inactive until this email address is verified/)).toBeTruthy()
})

test('grants and revokes beta access', async () => {
  let beta = false
  mockApp((path) => {
    if (path.startsWith('/v1/admin/users?')) return { users: [user({ betaAccess: beta })], total: 1 }
    if (path === '/v1/admin/users/user-test') return { user: user({ betaAccess: beta }) }
    return {}
  })
  api.mutate.mockImplementation(async (path: string, method: string) => {
    if (path.endsWith('/beta')) beta = method === 'POST'
    return {}
  })
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: 'Users' }))
  await fireEvent.click(await screen.findByText('tester@example.invalid'))
  await fireEvent.click(await screen.findByRole('button', { name: 'Grant beta' }))
  expect(api.mutate).toHaveBeenCalledWith('/v1/admin/users/user-test/beta', 'POST', undefined)
  expect(await screen.findByText('User account updated and the action was audited.')).toBeTruthy()
  await fireEvent.click(await screen.findByRole('button', { name: 'Revoke beta' }))
  expect(api.mutate).toHaveBeenLastCalledWith('/v1/admin/users/user-test/beta', 'DELETE', undefined)
})

test('suspends with a reason and unsuspends', async () => {
  let suspended = false
  vi.stubGlobal('prompt', vi.fn(() => 'chargeback'))
  mockApp((path) => {
    if (path.startsWith('/v1/admin/users?')) return { users: [user({ suspendedAt: suspended ? '2026-08-31T12:00:00Z' : undefined })], total: 1 }
    if (path === '/v1/admin/users/user-test') return { user: user({ suspendedAt: suspended ? '2026-08-31T12:00:00Z' : undefined }) }
    return {}
  })
  api.mutate.mockImplementation(async (path: string, method: string) => {
    if (path.endsWith('/suspend')) suspended = method === 'POST'
    return {}
  })
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: 'Users' }))
  await fireEvent.click(await screen.findByText('tester@example.invalid'))
  await fireEvent.click(await screen.findByRole('button', { name: 'Suspend' }))
  expect(api.mutate).toHaveBeenCalledWith('/v1/admin/users/user-test/suspend', 'POST', { reason: 'chargeback' })
  await fireEvent.click(await screen.findByRole('button', { name: 'Unsuspend' }))
  expect(api.mutate).toHaveBeenLastCalledWith('/v1/admin/users/user-test/suspend', 'DELETE', undefined)
})

test('deletes an account only after confirmation', async () => {
  const confirmMock = vi.fn(() => true)
  vi.stubGlobal('confirm', confirmMock)
  mockApp((path) => {
    if (path.startsWith('/v1/admin/users?')) return { users: [user()], total: 1 }
    if (path === '/v1/admin/users/user-test') return { user: user() }
    return {}
  }, { role: 'super', permissions: ['users:read', 'users:manage', 'users:delete'] })
  api.mutate.mockResolvedValue({})
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: 'Users' }))
  await fireEvent.click(await screen.findByText('tester@example.invalid'))
  await fireEvent.click(await screen.findByRole('button', { name: 'Delete account' }))
  expect(confirmMock).toHaveBeenCalledWith('Permanently delete tester@example.invalid? This cannot be undone.')
  expect(api.mutate).toHaveBeenCalledWith('/v1/admin/users/user-test', 'DELETE')
  expect(await screen.findByText('User account deleted.')).toBeTruthy()
})

test('keeps the account when deletion is cancelled', async () => {
  const confirmMock = vi.fn(() => false)
  vi.stubGlobal('confirm', confirmMock)
  mockApp((path) => {
    if (path.startsWith('/v1/admin/users?')) return { users: [user()], total: 1 }
    if (path === '/v1/admin/users/user-test') return { user: user() }
    return {}
  }, { role: 'super', permissions: ['users:read', 'users:manage', 'users:delete'] })
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: 'Users' }))
  await fireEvent.click(await screen.findByText('tester@example.invalid'))
  await fireEvent.click(await screen.findByRole('button', { name: 'Delete account' }))
  expect(confirmMock).toHaveBeenCalled()
  expect(api.mutate).not.toHaveBeenCalled()
})

test('readonly staff read users without edit controls', async () => {
  mockApp((path) => {
    if (path.startsWith('/v1/admin/users?')) return { users: [user()], total: 1 }
    if (path === '/v1/admin/users/user-test') return { user: user() }
    return {}
  }, { role: 'readonly', permissions: ['users:read'] })
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: 'Users' }))
  await fireEvent.click(await screen.findByText('tester@example.invalid'))
  expect(await screen.findByText('Created')).toBeTruthy()
  expect(screen.queryByRole('button', { name: 'Grant beta' })).toBeNull()
  expect(screen.queryByRole('button', { name: 'Revoke sessions' })).toBeNull()
  expect(screen.queryByRole('button', { name: 'Suspend' })).toBeNull()
  expect(screen.queryByRole('button', { name: 'Delete account' })).toBeNull()
  expect(screen.queryByRole('button', { name: 'Add to owner update ring' })).toBeNull()
})

test('searches users after the debounce', async () => {
  mockApp((path) => path.startsWith('/v1/admin/users?') ? { users: [user()], total: 1 } : {})
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: 'Users' }))
  await fireEvent.input(await screen.findByLabelText('Search users'), { target: { value: 'tester' } })
  await vi.waitFor(() => expect(api.request).toHaveBeenCalledWith('/v1/admin/users?query=tester&size=100&page=1'))
})
