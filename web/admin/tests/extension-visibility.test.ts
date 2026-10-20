import { cleanup, fireEvent, render, screen } from '@testing-library/svelte'
import { afterEach, expect, test, vi } from 'vitest'
import type { AdminAccount, ChromeListingState } from '../src/lib/types'
import App from '../src/App.svelte'

const fetchMock = vi.fn()
vi.stubGlobal('fetch', fetchMock)

const admin: AdminAccount = {
  id: 'super', email: 'owner@example.invalid', role: 'super', mfaVerified: true, suspended: false,
  createdAt: '2026-08-31T00:00:00Z', permissions: ['releases:write', 'flags:manage'],
}
const listing = 'https://chromewebstore.google.com/detail/abcdefghijklmnopabcdefghijklmnop'

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

function mockApi(states: (ChromeListingState | 'fail')[]) {
  let reads = 0
  const patches: string[] = []
  fetchMock.mockImplementation(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    const method = (init?.method || 'GET').toUpperCase()
    if (url.endsWith('/v1/admin/auth/me')) return jsonResponse({ admin, deploymentProfile: 'project' })
    if (url.endsWith('/v1/admin/auth/csrf')) return jsonResponse({ token: 'csrf-token' })
    if (url.endsWith('/v1/admin/overview')) return jsonResponse({ overview: {} })
    if (url.endsWith('/v1/admin/releases')) return jsonResponse({ releases: [] })
    if (url.endsWith('/v1/admin/extension-publications')) {
      const state = states[Math.min(reads, states.length - 1)]
      reads += 1
      if (state === 'fail') return jsonResponse({ error: { code: 'admin_store_unavailable', message: 'The admin store is unavailable.' } }, 503)
      return jsonResponse({ publications: [], chrome: state })
    }
    if (url.endsWith('/v1/admin/flags/browser_extension_visible') && method === 'PATCH') {
      patches.push(String(init?.body ?? ''))
      return new Response(null, { status: 204 })
    }
    return jsonResponse({ error: { code: 'not_found', message: 'not found' } }, 404)
  })
  return patches
}

async function turnOn() {
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: 'Releases' }))
  const select = await screen.findByRole('combobox', { name: 'Show the Chrome extension on the website' })
  await fireEvent.change(select, { target: { value: 'true' } })
  return select as HTMLSelectElement
}

afterEach(() => {
  cleanup()
  fetchMock.mockReset()
})

test('says the website links the store once the refreshed entry is published', async () => {
  const patches = mockApi([
    { listingUrl: listing, visible: false, public: [] },
    { listingUrl: listing, visible: true, public: [{ store: 'chrome', status: 'published', listingUrl: listing }] },
  ])
  await turnOn()
  expect(await screen.findByText('The website now links to the Chrome Web Store.')).toBeTruthy()
  expect(patches).toEqual([JSON.stringify({ value: 'true' })])
})

test('does not claim the listing is public when the server has no store id', async () => {
  mockApi([
    { listingUrl: '', visible: false, public: [] },
    { listingUrl: '', visible: true, public: [] },
  ])
  await turnOn()
  expect(await screen.findByText('The switch is on, but the website shows nothing until the server has the Chrome extension id.')).toBeTruthy()
  expect(screen.queryByText('The website now links to the Chrome Web Store.')).toBeNull()
})

test('keeps a failed refresh visible and leaves the switch on after the change saved', async () => {
  const patches = mockApi([{ listingUrl: listing, visible: false, public: [] }, 'fail'])
  const select = await turnOn()
  expect(await screen.findByText('The admin store is unavailable.')).toBeTruthy()
  expect(screen.queryByText(/The website now links|The switch is on/)).toBeNull()
  expect(select.value).toBe('true')
  expect(patches).toHaveLength(1)
})
