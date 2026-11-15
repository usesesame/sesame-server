import { cleanup, fireEvent, render, screen } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, test, vi } from 'vitest'
import type { AdminAccount, Overview } from '../src/lib/types'

const api = vi.hoisted(() => ({ request: vi.fn(), mutate: vi.fn() }))

vi.mock('../src/lib/api', () => {
  class APIError extends Error {
    constructor(message: string, public status: number, public code = '') { super(message) }
  }
  return { APIError, apiURL: 'https://api.test.invalid', request: api.request, mutate: api.mutate, onStepUpRequired: vi.fn() }
})

import App from '../src/App.svelte'
import { APIError } from '../src/lib/api'

const admin: AdminAccount = {
  id: 'ops',
  email: 'ops@example.invalid',
  role: 'support',
  mfaVerified: true,
  suspended: false,
  createdAt: '2026-01-01T00:00:00Z',
  permissions: ['support:read', 'support:manage'],
}

const overview: Overview = { users: 1284, newUsersThisWeek: 37, betaUsers: 412, unverifiedUsers: 58, suspendedUsers: 3, activeAdminSessions: 2, openTickets: 14, unassignedTickets: 5, urgentTickets: 1 }

afterEach(() => {
  cleanup()
  api.request.mockReset()
  api.mutate.mockReset()
  history.replaceState({}, '', '/')
})

function signedOut() {
  api.request.mockImplementation(async (path: string) => {
    if (path === '/v1/admin/auth/me') throw new APIError('Sign in.', 401, 'not_authenticated')
    return {}
  })
}

test('submits the admin sign-in when Enter is pressed in the code field', async () => {
  signedOut()
  api.mutate.mockRejectedValue(new APIError('Email, password, or code is incorrect.', 401, 'invalid_credentials'))
  const user = userEvent.setup()
  render(App)
  await user.type(await screen.findByLabelText('Email'), 'ops@example.invalid')
  await user.type(screen.getByLabelText('Password'), 'fictional password')
  await user.type(screen.getByLabelText('Six-digit code'), '123456{Enter}')
  expect(api.mutate).toHaveBeenCalledTimes(1)
  expect(api.mutate).toHaveBeenCalledWith('/v1/admin/auth/login', 'POST', { email: 'ops@example.invalid', password: 'fictional password', code: '123456' })
  expect((await screen.findByRole('alert')).textContent).toBe('Email, password, or code is incorrect.')
})

test('sends nothing when Enter is pressed before the code is complete', async () => {
  signedOut()
  const user = userEvent.setup()
  render(App)
  await user.type(await screen.findByLabelText('Email'), 'ops@example.invalid')
  await user.type(screen.getByLabelText('Password'), 'fictional password')
  await user.type(screen.getByLabelText('Six-digit code'), '123{Enter}')
  await fireEvent.submit(screen.getByRole('button', { name: 'Sign in' }).closest('form')!)
  expect(api.mutate).not.toHaveBeenCalled()
})

test('submits first-time setup when Enter is pressed', async () => {
  history.replaceState({}, '', '/?token=fictional-setup')
  api.mutate.mockImplementation(async (path: string) => {
    if (path === '/v1/admin/auth/setup/begin') return { email: 'new@example.invalid', secret: 'FICTIONALSECRET', uri: 'otpauth://totp/fictional' }
    throw new APIError('The code is incorrect.', 401, 'invalid_code')
  })
  const user = userEvent.setup()
  render(App)
  await user.type(await screen.findByLabelText('New password'), 'fictional long password')
  await user.type(screen.getByLabelText('Six-digit code'), '654321{Enter}')
  expect(api.mutate).toHaveBeenCalledWith('/v1/admin/auth/setup/complete', 'POST', { token: 'fictional-setup', password: 'fictional long password', code: '654321' })
})

test('lists what needs attention and opens the queue with that filter', async () => {
  api.request.mockImplementation(async (path: string) => {
    if (path === '/v1/admin/auth/me') return { admin }
    if (path === '/v1/admin/overview') return { overview }
    if (path.startsWith('/v1/admin/support?')) return { tickets: [], total: 0 }
    if (path === '/v1/admin/support/assignees') return { assignees: [] }
    if (path === '/v1/admin/saved-replies') return { savedReplies: [] }
    return {}
  })
  render(App)
  expect(await screen.findByRole('heading', { name: 'Needs you' })).toBeTruthy()
  expect(screen.getByRole('button', { name: '1 urgent ticket' })).toBeTruthy()
  expect(screen.getByRole('button', { name: '5 unassigned tickets' })).toBeTruthy()
  expect(screen.getByRole('button', { name: '14 tickets not closed yet' })).toBeTruthy()
  expect(screen.getByText('1,284')).toBeTruthy()
  expect(screen.getByText('New this week')).toBeTruthy()
  await fireEvent.click(screen.getByRole('button', { name: '1 urgent ticket' }))
  expect(await screen.findByRole('heading', { name: 'Support' })).toBeTruthy()
  expect(api.request).toHaveBeenCalledWith('/v1/admin/support?size=100&page=1&status=not_closed&priority=urgent')
  expect((screen.getByLabelText('Filter by priority') as HTMLSelectElement).value).toBe('urgent')
})

test('opens the unassigned queue from the overview', async () => {
  api.request.mockImplementation(async (path: string) => {
    if (path === '/v1/admin/auth/me') return { admin }
    if (path === '/v1/admin/overview') return { overview }
    if (path.startsWith('/v1/admin/support?')) return { tickets: [], total: 0 }
    return {}
  })
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: '5 unassigned tickets' }))
  expect(api.request).toHaveBeenCalledWith('/v1/admin/support?size=100&page=1&status=not_closed&assigned=unassigned')
})

test('shows one message when the overview cannot be loaded', async () => {
  api.request.mockImplementation(async (path: string) => {
    if (path === '/v1/admin/auth/me') return { admin }
    if (path === '/v1/admin/overview') throw new APIError('The overview is temporarily unavailable.', 503)
    return {}
  })
  render(App)
  expect((await screen.findByRole('alert')).textContent).toBe('The overview is temporarily unavailable.')
  expect(screen.getAllByRole('alert')).toHaveLength(1)
  expect(screen.queryByText('Not available')).toBeNull()
  expect(screen.queryByRole('heading', { name: 'Needs you' })).toBeNull()
})

test('each Needs you line opens the queue without closed tickets', async () => {
  api.request.mockImplementation(async (path: string) => {
    if (path === '/v1/admin/auth/me') return { admin }
    if (path === '/v1/admin/overview') return { overview: { ...overview, openTickets: 1, urgentTickets: 2, unassignedTickets: 3 } }
    if (path.startsWith('/v1/admin/support?')) return { tickets: [], total: 0 }
    return {}
  })
  const cases: [string, string][] = [
    ['1 ticket not closed yet', '/v1/admin/support?size=100&page=1&status=not_closed'],
    ['2 urgent tickets', '/v1/admin/support?size=100&page=1&status=not_closed&priority=urgent'],
    ['3 unassigned tickets', '/v1/admin/support?size=100&page=1&status=not_closed&assigned=unassigned'],
  ]
  for (const [name, path] of cases) {
    const view = render(App)
    await fireEvent.click(await screen.findByRole('button', { name }))
    expect(await screen.findByRole('heading', { name: 'Support' })).toBeTruthy()
    expect(api.request).toHaveBeenCalledWith(path)
    expect((screen.getByLabelText('Filter by status') as HTMLSelectElement).value).toBe('not_closed')
    view.unmount()
  }
})
