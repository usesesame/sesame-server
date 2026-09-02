import { cleanup, fireEvent, render, screen } from '@testing-library/svelte'
import { afterEach, expect, test, vi } from 'vitest'
import type { OperationalSnapshot } from '../src/lib/types'

const api = vi.hoisted(() => ({ request: vi.fn(), mutate: vi.fn() }))

vi.mock('../src/lib/api', () => {
  class APIError extends Error {
    constructor(message: string, public status: number, public code = '') { super(message) }
  }
  return { APIError, apiURL: 'https://api.test.invalid', request: api.request, mutate: api.mutate }
})

import App from '../src/App.svelte'
import { APIError } from '../src/lib/api'

const healthy: OperationalSnapshot = {
  api: { status: 'ready' }, version: { version: '1.2.3', commit: 'abc123' }, schema: { status: 'ready', version: '36' }, database: { status: 'ready', timedOut: false }, releasePipeline: { status: 'ready' }, artifactDelivery: { status: 'ready' }, emailOutbox: { status: 'ready', pending: 0, failed: 0 }, maintenance: { status: 'ready', lastRunAt: '2026-08-31T10:00:00Z' },
}

afterEach(() => { cleanup(); api.request.mockReset(); api.mutate.mockReset() })

function admin() {
  return { id: 'ops', email: 'ops@example.invalid', role: 'ops', mfaVerified: true, suspended: false, createdAt: '', permissions: ['system:read'] }
}

test('loads the operational snapshot without raw configuration', async () => {
  api.request.mockImplementation(async (path: string) => {
    if (path === '/v1/admin/auth/me') return { admin: admin() }
    if (path === '/v1/admin/overview') return { overview: {} }
    if (path === '/v1/admin/system/health') return healthy
    return {}
  })
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: 'System' }))
  expect(await screen.findByRole('status')).toBeTruthy()
  expect(api.request).toHaveBeenCalledWith('/v1/admin/system/health')
  expect(api.request).not.toHaveBeenCalledWith('/v1/admin/system/config')
  expect(api.request).not.toHaveBeenCalledWith('/v1/admin/system/rate-limits')
})

test.each([
  [new APIError('unavailable', 503), 'System status is unavailable'],
  [new APIError('forbidden', 403), 'System access changed'],
])('shows a safe failure state', async (failure, message) => {
  api.request.mockImplementation(async (path: string) => {
    if (path === '/v1/admin/auth/me') return { admin: admin() }
    if (path === '/v1/admin/overview') return { overview: {} }
    if (path === '/v1/admin/system/health') throw failure
    return {}
  })
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: 'System' }))
  expect((await screen.findByRole('alert')).textContent).toContain(message)
})

test('hides system navigation without permission', async () => {
  api.request.mockImplementation(async (path: string) => {
    if (path === '/v1/admin/auth/me') return { admin: { ...admin(), permissions: [] } }
    if (path === '/v1/admin/overview') return { overview: {} }
    return {}
  })
  render(App)
  await screen.findByRole('heading', { name: 'Overview' })
  expect(screen.queryByRole('button', { name: 'System' })).toBeNull()
})
