import { cleanup, render, screen } from '@testing-library/svelte'
import { afterEach, expect, test, vi } from 'vitest'
import App from '../src/App.svelte'
import type { AdminAccount } from '../src/lib/types'

const fetchMock = vi.fn()
vi.stubGlobal('fetch', fetchMock)

const admin: AdminAccount = {
  id: 'admin-profile',
  email: 'super@example.invalid',
  role: 'super',
  mfaVerified: true,
  suspended: false,
  createdAt: '2026-08-31T00:00:00Z',
  permissions: ['users:read', 'users:manage', 'users:delete', 'flags:manage', 'releases:write', 'plans:write', 'admins:manage', 'audit:all', 'system:read', 'support:manage', 'support:read'],
}

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

function mockIdentity(deploymentProfile: 'operator' | 'project') {
  fetchMock.mockImplementation(async (input: RequestInfo | URL) => {
    const url = String(input)
    if (url.includes('/v1/admin/auth/me')) return jsonResponse({ admin, deploymentProfile })
    if (url.includes('/v1/admin/overview')) return jsonResponse({ overview: {} })
    return jsonResponse({ error: { code: 'not_found', message: 'not found' } }, 404)
  })
}

afterEach(() => {
  cleanup()
  fetchMock.mockReset()
})

test('operator profile navigation hides releases and product plans', async () => {
  mockIdentity('operator')
  render(App)
  await screen.findByRole('button', { name: 'Overview' })
  expect(screen.queryByRole('button', { name: 'Releases' })).toBeNull()
  expect(screen.queryByRole('button', { name: 'Product plans' })).toBeNull()
})

test('project profile navigation shows releases and product plans', async () => {
  mockIdentity('project')
  render(App)
  await screen.findByRole('button', { name: 'Overview' })
  expect(await screen.findByRole('button', { name: 'Releases' })).toBeTruthy()
  expect(screen.getByRole('button', { name: 'Product plans' })).toBeTruthy()
})
