import { cleanup, fireEvent, render, screen } from '@testing-library/svelte'
import { afterEach, expect, test, vi } from 'vitest'
import ExtensionStoresWorkspace from '../src/lib/releases/ExtensionStoresWorkspace.svelte'
import type { ExtensionPublication } from '../src/lib/types'

function publication(overrides: Partial<ExtensionPublication> = {}): ExtensionPublication {
  return {
    id: 'publication-test', store: 'chrome', version: '0.1.0', packageSha256: 'a'.repeat(64), packageBytes: 48213,
    filename: 'sesame-extension-0.1.0-chrome.zip', status: 'built', evidence: {}, stateRevision: 1,
    createdAt: '2026-09-07T00:00:00Z', updatedAt: '2026-09-07T00:00:00Z', audit: [], ...overrides,
  }
}

afterEach(cleanup)

test('hides store controls without permission', () => {
  render(ExtensionStoresWorkspace, { publications: [publication()], canManage: false, onTransition: vi.fn(), onAccept: vi.fn() })
  expect(screen.queryByRole('button', { name: 'Mark uploaded' })).toBeNull()
  expect(screen.queryByRole('button', { name: 'Record built package' })).toBeNull()
})

test('offers only the next state for each status', () => {
  render(ExtensionStoresWorkspace, { publications: [publication({ status: 'submitted' })], canManage: true, onTransition: vi.fn(), onAccept: vi.fn() })
  expect(screen.getByRole('button', { name: 'Mark approved' })).toBeTruthy()
  expect(screen.queryByRole('button', { name: 'Mark submitted' })).toBeNull()
  expect(screen.queryByRole('button', { name: 'Mark withdrawn' })).toBeNull()
})

test('sends the transition with evidence and revision', async () => {
  const onTransition = vi.fn()
  render(ExtensionStoresWorkspace, { publications: [publication()], canManage: true, onTransition, onAccept: vi.fn() })
  await fireEvent.click(screen.getByRole('button', { name: 'Mark uploaded' }))
  await fireEvent.input(screen.getByPlaceholderText('Store listing or submission reference'), { target: { value: 'store item 12' } })
  await fireEvent.click(screen.getByRole('button', { name: 'Confirm' }))
  expect(onTransition).toHaveBeenCalledWith(expect.objectContaining({ id: 'publication-test', stateRevision: 1 }), 'uploaded', { reference: 'store item 12' })
})

test('withdrawn evidence is a reason', async () => {
  const onTransition = vi.fn()
  render(ExtensionStoresWorkspace, { publications: [publication({ status: 'published', stateRevision: 5 })], canManage: true, onTransition, onAccept: vi.fn() })
  await fireEvent.click(screen.getByRole('button', { name: 'Mark withdrawn' }))
  await fireEvent.input(screen.getByPlaceholderText('Why it was withdrawn'), { target: { value: 'store removal' } })
  await fireEvent.click(screen.getByRole('button', { name: 'Confirm' }))
  expect(onTransition).toHaveBeenCalledWith(expect.objectContaining({ id: 'publication-test', stateRevision: 5 }), 'withdrawn', { reason: 'store removal' })
})

test('records a built package with a valid digest and rejects an invalid one', async () => {
  const onAccept = vi.fn()
  render(ExtensionStoresWorkspace, { publications: [], canManage: true, onTransition: vi.fn(), onAccept })
  const version = screen.getByPlaceholderText('0.1.0') as HTMLInputElement
  const digest = screen.getByPlaceholderText('64 hex characters') as HTMLInputElement
  const bytes = screen.getByLabelText('Package bytes') as HTMLInputElement
  const filename = screen.getByPlaceholderText('sesame-extension-0.1.0-chrome.zip') as HTMLInputElement
  await fireEvent.input(version, { target: { value: '0.1.0' } })
  await fireEvent.input(digest, { target: { value: 'zz' } })
  await fireEvent.input(bytes, { target: { value: '48213' } })
  await fireEvent.input(filename, { target: { value: 'sesame-extension-0.1.0-chrome.zip' } })
  expect((screen.getByRole('button', { name: 'Record built package' }) as HTMLButtonElement).disabled).toBe(true)
  await fireEvent.input(digest, { target: { value: 'a'.repeat(64) } })
  expect((screen.getByRole('button', { name: 'Record built package' }) as HTMLButtonElement).disabled).toBe(false)
  await fireEvent.click(screen.getByRole('button', { name: 'Record built package' }))
  expect(onAccept).toHaveBeenCalledWith({ store: 'chrome', version: '0.1.0', packageSha256: 'a'.repeat(64), filename: 'sesame-extension-0.1.0-chrome.zip', packageBytes: 48213 })
})

test('shows evidence and audit history', () => {
  render(ExtensionStoresWorkspace, {
    publications: [publication({
      status: 'published', stateRevision: 5,
      evidence: { built: { runURL: 'https://ci.test.invalid/run/1' }, published: { reference: 'store listing' } },
      audit: [{ id: 1, adminEmail: 'ops@example.invalid', action: 'extension_publication.published', targetType: 'extension_publication', detail: {}, createdAt: '' }],
    })],
    canManage: false, onTransition: vi.fn(), onAccept: vi.fn(),
  })
  expect(screen.getByText(/built: https:\/\/ci.test.invalid\/run\/1/)).toBeTruthy()
  expect(screen.getByText('published by ops@example.invalid')).toBeTruthy()
})
