import { cleanup, fireEvent, render, screen } from '@testing-library/svelte'
import { afterEach, expect, test, vi } from 'vitest'
import ReleaseWorkspace from '../src/lib/releases/ReleaseWorkspace.svelte'
import type { Release } from '../src/lib/types'

function release(overrides: Partial<Release> = {}): Release {
  return {
    id: 'release-test', channel: 'beta', platform: 'windows', architecture: 'x86_64', version: '0.2.3', url: 'https://downloads.example.invalid/Sesame.exe', sha256: 'a'.repeat(64), signature: 's'.repeat(64), signingKeyId: 'test-key', supportedWindows: 'Windows 10', releaseNotesUrl: 'https://example.invalid/releases/0.2.3', rollbackNotice: '', status: 'draft', rolloutPercent: 100, updateEnabled: true, killSwitch: false, manifestRevision: 1, releaseSetDigest: 'b'.repeat(64), releaseSetVerifiedAt: '2026-08-31T00:00:00Z', artifacts: [], publicationBlockers: [], audit: [], ...overrides,
  }
}

afterEach(cleanup)

test('hides release controls without permission', () => {
  render(ReleaseWorkspace, { releases: [release()], canManage: false, onCommand: vi.fn() })
  expect(screen.queryByRole('button', { name: 'Publish' })).toBeNull()
})

test('blocks degraded evidence', () => {
  render(ReleaseWorkspace, { releases: [release({ publicationBlockers: ['verified eligible artifact'] })], canManage: true, onCommand: vi.fn() })
  expect((screen.getByRole('button', { name: 'Publish' }) as HTMLButtonElement).disabled).toBe(true)
  expect(screen.getByText('Publishing needs verified eligible artifact.')).toBeTruthy()
})

test('shows release audit history', () => {
  render(ReleaseWorkspace, { releases: [release({ audit: [{ id: 1, adminEmail: 'ops@example.invalid', action: 'release.publish', targetType: 'release', detail: {}, createdAt: '' }] })], canManage: false, onCommand: vi.fn() })
  expect(screen.getByText('publish by ops@example.invalid')).toBeTruthy()
})

test('shows every package in a verified release set', () => {
  const artifact = { id: 'artifact-test', format: 'nsis' as const, architecture: 'x86_64' as const, url: 'https://downloads.example.invalid/Sesame.exe', sha256: 'a'.repeat(64), bytes: 42, updaterCapable: true, updaterSignature: 's'.repeat(64), updaterSigningKeyId: 'updater-test', distributionClass: 'early_access' as const, sigstoreVerified: true, authenticodeVerified: false, verifiedAt: '2026-08-31T00:00:00Z' }
  render(ReleaseWorkspace, { releases: [release({ artifacts: [artifact] })], canManage: false, onCommand: vi.fn() })
  expect(screen.getByText('nsis (x86_64)')).toBeTruthy()
  expect(screen.getByText(/updater capable/)).toBeTruthy()
})

test('confirms publication and emergency stop', async () => {
  const onCommand = vi.fn()
  const { rerender } = render(ReleaseWorkspace, { releases: [release()], canManage: true, onCommand })
  await fireEvent.click(screen.getByRole('button', { name: 'Publish' }))
  expect(onCommand).not.toHaveBeenCalled()
  await fireEvent.click(screen.getByRole('button', { name: 'Confirm' }))
  expect(onCommand).toHaveBeenCalledWith(expect.objectContaining({ id: 'release-test' }), 'publish')
  rerender({ releases: [release({ status: 'published' })], canManage: true, onCommand })
  await fireEvent.click(screen.getByRole('button', { name: 'Emergency stop' }))
  await fireEvent.click(screen.getByRole('button', { name: 'Confirm' }))
  expect(onCommand).toHaveBeenLastCalledWith(expect.objectContaining({ id: 'release-test' }), 'emergency-stop')
})

test('sends rollout without immutable fields', async () => {
  const onCommand = vi.fn()
  render(ReleaseWorkspace, { releases: [release({ status: 'published', rolloutPercent: 25 })], canManage: true, onCommand })
  await fireEvent.click(screen.getByRole('button', { name: 'Set rollout' }))
  expect(onCommand).toHaveBeenCalledWith(expect.objectContaining({ manifestRevision: 1, rolloutPercent: 25 }), 'rollout')
})
