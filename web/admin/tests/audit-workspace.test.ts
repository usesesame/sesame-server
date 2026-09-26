import { cleanup, fireEvent, render, screen } from '@testing-library/svelte'
import { afterEach, expect, test, vi } from 'vitest'
import type { AdminAccount, AuditEntry } from '../src/lib/types'

const api = vi.hoisted(() => ({ request: vi.fn(), mutate: vi.fn() }))

vi.mock('../src/lib/api', () => {
  class APIError extends Error {
    constructor(message: string, public status: number, public code = '') { super(message) }
  }
  return { APIError, apiURL: 'https://api.test.invalid', request: api.request, mutate: api.mutate }
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
    permissions: ['audit:all'],
    ...overrides,
  }
}

function entry(overrides: Partial<AuditEntry> = {}): AuditEntry {
  return {
    id: 1,
    adminEmail: 'ops@example.invalid',
    action: 'user.suspend',
    targetType: 'user',
    targetId: 'user-test',
    detail: {},
    createdAt: '2026-08-31T10:00:00Z',
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
  vi.restoreAllMocks()
  delete (URL as unknown as Record<string, unknown>).createObjectURL
  delete (URL as unknown as Record<string, unknown>).revokeObjectURL
})

test('loads the full audit log and renders deleted administrators', async () => {
  mockApp((path) => path.startsWith('/v1/admin/audit?')
    ? { entries: [entry(), entry({ id: 2, adminEmail: '', action: 'user.delete', targetType: 'user', targetId: undefined })] }
    : {})
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: 'Audit log' }))
  expect(await screen.findByText('user.suspend')).toBeTruthy()
  expect(api.request).toHaveBeenCalledWith('/v1/admin/audit?size=100')
  expect(screen.getByText('ops@example.invalid', { selector: 'td' })).toBeTruthy()
  expect(screen.getByText('user user-test')).toBeTruthy()
  expect(screen.getByText('Deleted admin')).toBeTruthy()
  expect(screen.getByText('2 results on this page')).toBeTruthy()
})

test('scopes the audit log to the operator without full access', async () => {
  mockApp((path) => path.startsWith('/v1/admin/audit/me?')
    ? { entries: [entry({ action: 'user.suspend' })] }
    : {}, { role: 'support', permissions: [] })
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: 'Audit log' }))
  expect(await screen.findByText('user.suspend')).toBeTruthy()
  expect(api.request).toHaveBeenCalledWith('/v1/admin/audit/me?size=100')
  expect(screen.queryByLabelText('Admin ID')).toBeNull()
  expect(screen.queryByRole('button', { name: 'Export CSV' })).toBeNull()
})

test('applies audit filters to the request', async () => {
  mockApp((path) => path.startsWith('/v1/admin/audit?') ? { entries: [] } : {})
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: 'Audit log' }))
  await fireEvent.input(await screen.findByLabelText('Action'), { target: { value: 'user.suspend' } })
  await fireEvent.input(screen.getByLabelText('Admin ID'), { target: { value: 'ops' } })
  await fireEvent.input(screen.getByLabelText('From'), { target: { value: '2026-08-01T10:00' } })
  await fireEvent.input(screen.getByLabelText('To'), { target: { value: '2026-08-31T10:00' } })
  await fireEvent.click(screen.getByRole('button', { name: 'Apply filters' }))
  const query = new URLSearchParams({
    size: '100',
    action: 'user.suspend',
    admin: 'ops',
    from: new Date('2026-08-01T10:00').toISOString(),
    to: new Date('2026-08-31T10:00').toISOString(),
  }).toString()
  expect(api.request).toHaveBeenCalledWith(`/v1/admin/audit?${query}`)
})

test('exports the audit log as a CSV download', async () => {
  const fetchMock = vi.fn(async () => new Response(new Blob(['time,action\n'], { type: 'text/csv' }), { status: 200 }))
  vi.stubGlobal('fetch', fetchMock)
  const createObjectURL = vi.fn(() => 'blob:audit')
  const revokeObjectURL = vi.fn()
  Object.assign(URL, { createObjectURL, revokeObjectURL })
  const click = vi.spyOn(HTMLElement.prototype, 'click').mockImplementation(() => {})
  mockApp((path) => path.startsWith('/v1/admin/audit?') ? { entries: [] } : {})
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: 'Audit log' }))
  await fireEvent.click(await screen.findByRole('button', { name: 'Export CSV' }))
  await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledWith('https://api.test.invalid/v1/admin/audit/export?size=100', { credentials: 'include' }))
  await vi.waitFor(() => expect(createObjectURL).toHaveBeenCalled())
  expect(revokeObjectURL).toHaveBeenCalledWith('blob:audit')
  expect((click.mock.instances[0] as HTMLAnchorElement).download).toBe('sesame-admin-audit.csv')
})
