import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte'
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

test('suspends with a reason after the inline confirmation and unsuspends', async () => {
  let suspended = false
  const promptMock = vi.fn(() => 'unused')
  vi.stubGlobal('prompt', promptMock)
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
  expect(api.mutate).not.toHaveBeenCalled()
  expect(screen.getByText('Suspend tester@example.invalid? Every website session ends now, and the account stays suspended until someone unsuspends it.')).toBeTruthy()
  await fireEvent.input(screen.getByLabelText('Reason, shown only to staff'), { target: { value: 'chargeback' } })
  await fireEvent.click(screen.getByRole('button', { name: 'Suspend account' }))
  expect(promptMock).not.toHaveBeenCalled()
  expect(api.mutate).toHaveBeenCalledWith('/v1/admin/users/user-test/suspend', 'POST', { reason: 'chargeback' })
  await fireEvent.click(await screen.findByRole('button', { name: 'Unsuspend' }))
  expect(api.mutate).toHaveBeenLastCalledWith('/v1/admin/users/user-test/suspend', 'DELETE', undefined)
})

test('deletes an account only after the inline confirmation', async () => {
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
  expect(api.mutate).not.toHaveBeenCalled()
  expect(screen.getByText('Delete tester@example.invalid permanently? This removes the account and cannot be undone.')).toBeTruthy()
  await fireEvent.click(screen.getByRole('button', { name: 'Delete permanently' }))
  expect(confirmMock).not.toHaveBeenCalled()
  expect(api.mutate).toHaveBeenCalledWith('/v1/admin/users/user-test', 'DELETE')
  expect(await screen.findByText('User account deleted.')).toBeTruthy()
})

test('keeps the account when deletion is cancelled', async () => {
  mockApp((path) => {
    if (path.startsWith('/v1/admin/users?')) return { users: [user()], total: 1 }
    if (path === '/v1/admin/users/user-test') return { user: user() }
    return {}
  }, { role: 'super', permissions: ['users:read', 'users:manage', 'users:delete'] })
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: 'Users' }))
  await fireEvent.click(await screen.findByText('tester@example.invalid'))
  await fireEvent.click(await screen.findByRole('button', { name: 'Delete account' }))
  await fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
  expect(screen.queryByRole('button', { name: 'Delete permanently' })).toBeNull()
  expect(api.mutate).not.toHaveBeenCalled()
})

test('cancelling a suspension sends nothing', async () => {
  mockApp((path) => {
    if (path.startsWith('/v1/admin/users?')) return { users: [user()], total: 1 }
    if (path === '/v1/admin/users/user-test') return { user: user() }
    return {}
  })
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: 'Users' }))
  await fireEvent.click(await screen.findByText('tester@example.invalid'))
  await fireEvent.click(await screen.findByRole('button', { name: 'Suspend' }))
  await fireEvent.input(screen.getByLabelText('Reason, shown only to staff'), { target: { value: 'chargeback' } })
  await fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
  expect(screen.queryByLabelText('Reason, shown only to staff')).toBeNull()
  expect(api.mutate).not.toHaveBeenCalled()
})

test('revokes all website sessions only after the inline confirmation', async () => {
  mockApp((path) => {
    if (path.startsWith('/v1/admin/users?')) return { users: [user()], total: 1 }
    if (path === '/v1/admin/users/user-test') return { user: user() }
    return {}
  })
  api.mutate.mockResolvedValue({})
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: 'Users' }))
  await fireEvent.click(await screen.findByText('tester@example.invalid'))
  await fireEvent.click(await screen.findByRole('button', { name: 'Revoke sessions' }))
  expect(api.mutate).not.toHaveBeenCalled()
  expect(screen.getByText('Sign tester@example.invalid out of every website session? They can sign in again.')).toBeTruthy()
  await fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
  expect(api.mutate).not.toHaveBeenCalled()
  await fireEvent.click(screen.getByRole('button', { name: 'Revoke sessions' }))
  await fireEvent.click(screen.getByRole('button', { name: 'Revoke all sessions' }))
  expect(api.mutate).toHaveBeenCalledTimes(1)
  expect(api.mutate).toHaveBeenCalledWith('/v1/admin/users/user-test/sessions', 'DELETE', undefined)
  expect(await screen.findByText('User account updated and the action was audited.')).toBeTruthy()
  await vi.waitFor(() => expect(screen.queryByRole('button', { name: 'Revoke all sessions' })).toBeNull())
})

test('revokes a connected device only after the inline confirmation', async () => {
  const detail = user({ devices: [{ id: 'device-test', name: 'Office PC', connectedAt: '2026-08-01T00:00:00Z', expiresAt: '2026-09-01T00:00:00Z' }] })
  mockApp((path) => {
    if (path.startsWith('/v1/admin/users?')) return { users: [user()], total: 1 }
    if (path === '/v1/admin/users/user-test') return { user: detail }
    return {}
  })
  api.mutate.mockResolvedValue({})
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: 'Users' }))
  await fireEvent.click(await screen.findByText('tester@example.invalid'))
  await fireEvent.click(await screen.findByRole('button', { name: 'Revoke' }))
  expect(api.mutate).not.toHaveBeenCalled()
  expect(screen.getByText('Revoke Office PC? The desktop is disconnected from this account and has to be linked again.')).toBeTruthy()
  await fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
  expect(api.mutate).not.toHaveBeenCalled()
  await fireEvent.click(screen.getByRole('button', { name: 'Revoke' }))
  await fireEvent.click(screen.getByRole('button', { name: 'Revoke device' }))
  expect(api.mutate).toHaveBeenCalledTimes(1)
  expect(api.mutate).toHaveBeenCalledWith('/v1/admin/users/user-test/devices/device-test', 'DELETE', undefined)
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

test('locks every user action while an unsuspend request is in flight', async () => {
  mockApp((path) => {
    if (path.startsWith('/v1/admin/users?')) return { users: [user({ suspendedAt: '2026-08-31T12:00:00Z' })], total: 1 }
    if (path === '/v1/admin/users/user-test') return { user: user({ suspendedAt: '2026-08-31T12:00:00Z' }) }
    return {}
  }, { role: 'super', permissions: ['users:read', 'users:manage', 'users:delete'] })
  let release: (value: unknown) => void = () => {}
  api.mutate.mockImplementation(() => new Promise((done) => { release = done }))
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: 'Users' }))
  await fireEvent.click(await screen.findByText('tester@example.invalid'))
  await fireEvent.click(await screen.findByRole('button', { name: 'Delete account' }))
  await fireEvent.click(screen.getByRole('button', { name: 'Unsuspend' }))
  expect(api.mutate).toHaveBeenCalledTimes(1)
  expect(api.mutate).toHaveBeenCalledWith('/v1/admin/users/user-test/suspend', 'DELETE', undefined)
  for (const name of ['Unsuspend', 'Grant beta', 'Revoke sessions', 'Delete account', 'Delete permanently']) {
    expect((screen.getByRole('button', { name }) as HTMLButtonElement).disabled).toBe(true)
  }
  await fireEvent.click(screen.getByRole('button', { name: 'Delete permanently' }))
  await fireEvent.click(screen.getByRole('button', { name: 'Unsuspend' }))
  expect(api.mutate).toHaveBeenCalledTimes(1)
  release({})
  expect(await screen.findByText('User account updated and the action was audited.')).toBeTruthy()
  await waitFor(() => expect((screen.getByRole('button', { name: 'Delete permanently' }) as HTMLButtonElement).disabled).toBe(false))
})

test('locks the user actions while a deletion is in flight', async () => {
  mockApp((path) => {
    if (path.startsWith('/v1/admin/users?')) return { users: [user()], total: 1 }
    if (path === '/v1/admin/users/user-test') return { user: user() }
    return {}
  }, { role: 'super', permissions: ['users:read', 'users:manage', 'users:delete'] })
  let release: (value: unknown) => void = () => {}
  api.mutate.mockImplementation(() => new Promise((done) => { release = done }))
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: 'Users' }))
  await fireEvent.click(await screen.findByText('tester@example.invalid'))
  await fireEvent.click(await screen.findByRole('button', { name: 'Delete account' }))
  await fireEvent.click(screen.getByRole('button', { name: 'Delete permanently' }))
  expect(api.mutate).toHaveBeenCalledTimes(1)
  const suspend = screen.getByRole('button', { name: 'Suspend' }) as HTMLButtonElement
  expect(suspend.disabled).toBe(true)
  expect((screen.getByRole('button', { name: 'Grant beta' }) as HTMLButtonElement).disabled).toBe(true)
  await fireEvent.click(suspend)
  await fireEvent.click(screen.getByRole('button', { name: 'Grant beta' }))
  expect(api.mutate).toHaveBeenCalledTimes(1)
  release({})
  expect(await screen.findByText('User account deleted.')).toBeTruthy()
})
