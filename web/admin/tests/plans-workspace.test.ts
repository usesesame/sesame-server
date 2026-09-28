import { cleanup, fireEvent, render, screen } from '@testing-library/svelte'
import { afterEach, expect, test, vi } from 'vitest'
import type { AdminAccount, Plan } from '../src/lib/types'

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
    id: 'billing',
    email: 'billing@example.invalid',
    role: 'billing',
    mfaVerified: true,
    suspended: false,
    createdAt: '2026-01-01T00:00:00Z',
    permissions: ['plans:write'],
    ...overrides,
  }
}

function plan(overrides: Partial<Plan> = {}): Plan {
  return {
    id: 'plan-starter',
    name: 'Starter',
    price: '4.99',
    annualPrice: '49.99',
    billing: 'one_time',
    description: 'One-time licence for the desktop app.',
    available: true,
    includes: ['Desktop app'],
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
})

test('loads product plans with their billing state', async () => {
  mockApp((path) => path === '/v1/admin/plans' ? { plans: [plan()] } : {})
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: 'Product plans' }))
  expect(await screen.findByRole('heading', { name: 'Starter' })).toBeTruthy()
  expect(api.request).toHaveBeenCalledWith('/v1/admin/plans')
  expect((screen.getByLabelText('Price') as HTMLInputElement).value).toBe('4.99')
  expect((screen.getByLabelText('Annual price') as HTMLInputElement).value).toBe('49.99')
  expect((screen.getByLabelText('Billing') as HTMLSelectElement).value).toBe('one_time')
  expect((screen.getByLabelText('Available') as HTMLInputElement).checked).toBe(true)
})

test('saves plan changes', async () => {
  mockApp((path) => path === '/v1/admin/plans' ? { plans: [plan()] } : {})
  api.mutate.mockResolvedValue({})
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: 'Product plans' }))
  await fireEvent.input(await screen.findByLabelText('Name'), { target: { value: 'Starter Plus' } })
  await fireEvent.input(screen.getByLabelText('Price'), { target: { value: '5.99' } })
  await fireEvent.click(screen.getByLabelText('Available'))
  await fireEvent.click(screen.getByRole('button', { name: 'Save plan' }))
  expect(api.mutate).toHaveBeenCalledWith('/v1/admin/plans/plan-starter', 'PATCH', expect.objectContaining({
    id: 'plan-starter',
    name: 'Starter Plus',
    price: '5.99',
    available: false,
  }))
  expect(await screen.findByText('Starter Plus saved.')).toBeTruthy()
})

test('readonly staff cannot edit plans', async () => {
  mockApp((path) => path === '/v1/admin/plans' ? { plans: [plan()] } : {}, { role: 'readonly', permissions: [] })
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: 'Product plans' }))
  expect((await screen.findByLabelText('Name') as HTMLInputElement).disabled).toBe(true)
  expect((screen.getByLabelText('Price') as HTMLInputElement).disabled).toBe(true)
  expect((screen.getByLabelText('Available') as HTMLInputElement).disabled).toBe(true)
  expect(screen.queryByRole('button', { name: 'Save plan' })).toBeNull()
})
