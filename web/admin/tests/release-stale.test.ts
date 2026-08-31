import { cleanup, fireEvent, render, screen } from '@testing-library/svelte'
import { afterEach, expect, test, vi } from 'vitest'

const api = vi.hoisted(() => ({ request: vi.fn(), mutate: vi.fn() }))

vi.mock('../src/lib/api', () => {
  class APIError extends Error {
    constructor(message: string, public status: number, public code = '') { super(message) }
  }
  return { APIError, apiURL: 'https://api.test.invalid', request: api.request, mutate: api.mutate }
})

import App from '../src/App.svelte'

afterEach(() => { cleanup(); api.request.mockReset(); api.mutate.mockReset() })

test('reloads releases after a stale command', async () => {
  const release = { id: 'release-test', channel: 'beta', platform: 'windows', architecture: 'x86_64', version: '0.2.3', url: 'https://downloads.example.invalid/Sesame.exe', sha256: 'a'.repeat(64), signature: 's'.repeat(64), signingKeyId: 'test-key', supportedWindows: 'Windows 10', releaseNotesUrl: 'https://example.invalid/releases/0.2.3', rollbackNotice: '', status: 'draft', rolloutPercent: 100, updateEnabled: true, killSwitch: false, manifestRevision: 1, publicationBlockers: [], audit: [] }
  api.request.mockImplementation(async (path: string) => {
    if (path === '/v1/admin/auth/me') return { admin: { id: 'ops', email: 'ops@example.invalid', role: 'ops', mfaVerified: true, suspended: false, createdAt: '', permissions: ['releases:write'] } }
    if (path === '/v1/admin/overview') return { overview: {} }
    if (path === '/v1/admin/releases') return { releases: [release] }
    return {}
  })
  api.mutate.mockRejectedValueOnce(new (await import('../src/lib/api')).APIError('stale', 409, 'release_manifest_conflict'))
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: 'Releases' }))
  await fireEvent.click(await screen.findByRole('button', { name: 'Publish' }))
  await fireEvent.click(await screen.findByRole('button', { name: 'Confirm' }))
  expect(await screen.findByText('Release state reloaded. Try the command again.')).toBeTruthy()
  expect(api.request).toHaveBeenCalledWith('/v1/admin/releases')
})
