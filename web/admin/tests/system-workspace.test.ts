import { cleanup, render, screen } from '@testing-library/svelte'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import SystemWorkspace from '../src/lib/system/SystemWorkspace.svelte'
import type { OperationalSnapshot } from '../src/lib/types'

function snapshot(overrides: Partial<OperationalSnapshot> = {}): OperationalSnapshot {
  return {
    api: { status: 'ready' },
    version: { version: '1.2.3', commit: 'abc123' },
    schema: { status: 'ready', version: '36' },
    database: { status: 'ready', timedOut: false },
    releasePipeline: { status: 'ready' },
    artifactDelivery: { status: 'ready' },
    emailOutbox: { status: 'ready', pending: 2, failed: 0 },
    maintenance: { status: 'ready', lastRunAt: '2026-08-31T10:00:00Z' },
    ...overrides,
  }
}

beforeEach(() => vi.setSystemTime(new Date('2026-08-31T12:00:00Z')))
afterEach(() => { cleanup(); vi.useRealTimers() })

test('shows a healthy operational snapshot', () => {
  render(SystemWorkspace, { snapshot: snapshot(), failure: '' })
  expect(screen.getByRole('status').textContent).toContain('System is operating normally.')
  expect(screen.getByText('1.2.3')).toBeTruthy()
  expect(screen.getByText('2 pending, 0 failed')).toBeTruthy()
  expect(screen.getByText('Last ran 2 hours ago')).toBeTruthy()
  expect(screen.queryAllByRole('button')).toHaveLength(0)
})

test('names degraded dependencies and next actions', () => {
  render(SystemWorkspace, { snapshot: snapshot({ database: { status: 'unavailable', timedOut: true }, emailOutbox: { status: 'degraded', pending: 7, failed: 3 } }), failure: '' })
  expect(screen.getByRole('status').textContent).toContain('System needs attention.')
  expect(screen.getByText('Readiness check timed out')).toBeTruthy()
  expect(screen.getByText('Check PostgreSQL availability from protected operations.')).toBeTruthy()
  expect(screen.getByText('7 pending, 3 failed')).toBeTruthy()
  expect(screen.getByText('Check the email worker and failed deliveries.')).toBeTruthy()
})

test('shows an unavailable snapshot', () => {
  render(SystemWorkspace, { snapshot: null, failure: 'unavailable' })
  expect(screen.getByRole('alert').textContent).toContain('System status is unavailable')
  expect(screen.getByRole('alert').textContent).toContain('Check API readiness')
})

test('shows an unauthorized snapshot', () => {
  render(SystemWorkspace, { snapshot: null, failure: 'unauthorized' })
  expect(screen.getByRole('alert').textContent).toContain('System access changed')
  expect(screen.getByRole('alert').textContent).toContain('no longer has permission')
})
