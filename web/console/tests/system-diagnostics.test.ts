import { fireEvent, screen } from '@testing-library/svelte'
import { expect, test } from 'vitest'
import { apiError, type FakeCall } from './support/fake-api'
import { openApp, useCleanApp } from './support/app'

useCleanApp()

const system = (overrides: Record<string, unknown> = {}) => ({
  version: '0.1.0',
  commit: 'unknown',
  schemaVersion: 3,
  databaseBytes: 2048,
  lastBackupAt: null,
  warnings: [] as string[],
  auditChainOk: true,
  ...overrides,
})

const report = (overrides: Record<string, unknown> = {}) => ({
  schemaVersion: 1,
  generatedAt: '2026-11-08T10:00:00Z',
  overall: 'action',
  checks: [
    { id: 'backup', state: 'action', code: 'backup_missing', summary: 'No backup has been recorded.', next: 'Set a backup schedule on the host.' },
    { id: 'origin', state: 'ok', code: 'origin_local', summary: 'The public address is a loopback HTTP origin.', next: null },
  ],
  limitations: ['network_reachability_not_probed'],
  ...overrides,
})

async function openSystem(routes: Parameters<typeof openApp>[1] = {}) {
  const fake = openApp('/', routes)
  await fireEvent.click(await screen.findByRole('link', { name: 'System' }))
  await screen.findByRole('heading', { name: 'System' })
  return fake
}

test('shows diagnostics and hides only notices the report covers', async () => {
  await openSystem({
    'GET /v1/owner/system': () => system({
      warnings: ['No backup has been made yet.', 'The configuration file is readable by other users.', 'A future notice.'],
      warningCodes: ['backup', 'configuration', 'future'],
    }),
    'GET /v1/owner/system/diagnostics': () => report(),
  })
  expect(await screen.findByText('Backups')).toBeTruthy()
  expect(screen.getByText('No backup has been recorded.')).toBeTruthy()
  expect(screen.queryByText('No backup has been made yet.')).toBeNull()
  expect(screen.getByText('The configuration file is readable by other users.')).toBeTruthy()
  expect(screen.getByText('A future notice.')).toBeTruthy()
  expect(screen.getByText('Overall: Action needed')).toBeTruthy()
  expect(screen.getByText('Network reachability was not probed.')).toBeTruthy()
})

test('shows all six checks with visible state words and mapped limitations', async () => {
  await openSystem({
    'GET /v1/owner/system': () => system(),
    'GET /v1/owner/system/diagnostics': () => report({
      checks: [
        { id: 'origin', state: 'ok', code: 'origin_https', summary: 'The configured public address uses HTTPS.', next: null },
        { id: 'proxy', state: 'ok', code: 'proxy_configured', summary: 'Trusted proxy ranges are configured.', next: null },
        { id: 'audit', state: 'ok', code: 'audit_incremental_ok', summary: 'The incremental audit verification found no break.', next: null },
        { id: 'backup', state: 'action', code: 'backup_future', summary: 'The last recorded backup time is in the future.', next: 'Check the host clock.' },
        { id: 'forwarding', state: 'ok', code: 'forwarding_not_observed', summary: 'No untrusted forwarded request was observed.', next: null },
        { id: 'external', state: 'unknown', code: 'external_not_inspected', summary: 'Certificates were not inspected.', next: null },
      ],
      limitations: ['certificate_validity_not_checked', 'network_reachability_not_probed', 'host_configuration_not_inspected', 'audit_uses_incremental_verification', 'backup_off_host_copy_not_verified', 'recovery_drill_not_verified', 'unseen_traffic_not_evaluated'],
    }),
  })
  expect(await screen.findByText('Public address')).toBeTruthy()
  expect(screen.getByText('Proxy trust')).toBeTruthy()
  expect(screen.getByText('Audit log', { selector: '.diagnostics-row strong' })).toBeTruthy()
  expect(screen.getByText('Backups')).toBeTruthy()
  expect(screen.getByText('Forwarding')).toBeTruthy()
  expect(screen.getByText('External checks')).toBeTruthy()
  expect(screen.getByText('Overall: Action needed')).toBeTruthy()
  expect(screen.getAllByText('OK')).toHaveLength(4)
  expect(screen.getAllByText('Action needed')).toHaveLength(1)
  expect(screen.getAllByText('Unknown')).toHaveLength(1)
  expect(screen.getByText('Certificate validity was not checked.')).toBeTruthy()
  expect(screen.getByText('The audit check uses incremental verification, so it does not recheck every past entry.')).toBeTruthy()
})

test('maps notices by code rather than by English text', async () => {
  await openSystem({
    'GET /v1/owner/system': () => system({
      warnings: ['No backup has been made yet.', 'A sentence that says nothing about backups.'],
      warningCodes: ['future', 'backup'],
    }),
    'GET /v1/owner/system/diagnostics': () => report(),
  })
  expect(await screen.findByText('Backups')).toBeTruthy()
  expect(screen.getByText('No backup has been made yet.')).toBeTruthy()
  expect(screen.queryByText('A sentence that says nothing about backups.')).toBeNull()
})

test('keeps facts and every notice when the diagnostics request fails', async () => {
  const { calls } = await openSystem({
    'GET /v1/owner/system': () => system({
      version: '0.2.0',
      commit: 'abc123456789',
      warnings: ['No backup has been made yet.'],
      warningCodes: ['backup'],
    }),
    'GET /v1/owner/system/diagnostics': () => apiError(503, 'unavailable', 'The server could not complete that action.'),
  })
  expect(await screen.findByText('The diagnostics report could not be loaded. The system information is still shown.')).toBeTruthy()
  expect(screen.getByText('0.2.0, commit abc1234')).toBeTruthy()
  expect(screen.getByText('2.0 KB')).toBeTruthy()
  expect(screen.getByText('No backup has been made yet.')).toBeTruthy()
  expect(screen.getByRole('button', { name: 'Export data' })).toBeTruthy()
  expect(document.querySelectorAll('.primary')).toHaveLength(0)
  const call = calls.find((item) => item.path === '/v1/owner/system/diagnostics') as FakeCall
  expect(call.method).toBe('GET')
  expect(call.body).toBeUndefined()
  expect(calls.filter((item) => item.path.startsWith('/v1/owner/system/diagnostics')).length).toBe(1)
})

test('keeps all notices and facts with an older server', async () => {
  await openSystem({
    'GET /v1/owner/system': () => system({
      lastBackupAt: null,
      warnings: ['No backup has been made yet.', 'Requests from a private address carry X-Forwarded-For.'],
    }),
    'GET /v1/owner/system/diagnostics': () => apiError(404, 'not_found', 'The requested API endpoint does not exist.'),
  })
  expect(await screen.findByText('This server does not provide the diagnostics report.')).toBeTruthy()
  expect(screen.getByText('No backup has been made yet.')).toBeTruthy()
  expect(screen.getByText('Requests from a private address carry X-Forwarded-For.')).toBeTruthy()
  expect(screen.getByText('No backup yet')).toBeTruthy()
})

test('keeps all notices when the report is malformed', async () => {
  await openSystem({
    'GET /v1/owner/system': () => system({
      warnings: ['No backup has been made yet.', 'A configuration warning.'],
      warningCodes: ['backup', 'configuration'],
    }),
    'GET /v1/owner/system/diagnostics': () => ({ schemaVersion: 2, generatedAt: '2026-11-08T10:00:00Z', overall: 'ok', checks: [], limitations: [] }),
  })
  expect(await screen.findByText('The diagnostics report could not be read. The system information is still shown.')).toBeTruthy()
  expect(screen.getByText('No backup has been made yet.')).toBeTruthy()
  expect(screen.getByText('A configuration warning.')).toBeTruthy()
  expect(screen.queryByText('Public address')).toBeNull()
})

test('shows unknown future checks and states as unknown', async () => {
  await openSystem({
    'GET /v1/owner/system': () => system({
      warnings: ['The audit log chain does not verify.', 'No backup has been made yet.'],
      warningCodes: ['audit', 'backup'],
    }),
    'GET /v1/owner/system/diagnostics': () => report({
      overall: 'ok',
      checks: [
        { id: 'origin', state: 'ok', code: 'origin_local', summary: 'The public address is a loopback HTTP origin.', next: null },
        { id: 'future_check', state: 'ok', code: 'future_ok', summary: 'A future check reported success.', next: null },
        { id: 'audit', state: 'review', code: 'audit_review', summary: 'A future audit state was reported.', next: null },
      ],
    }),
  })
  expect(await screen.findByText('Other check')).toBeTruthy()
  expect(screen.getByText('Overall: Unknown')).toBeTruthy()
  expect(screen.getByText('A future check reported success.').closest('.diagnostics-row')?.getAttribute('data-state')).toBe('unknown')
  const auditRow = [...document.querySelectorAll('.diagnostics-row')].find((row) => row.querySelector('strong')?.textContent === 'Audit log')
  expect(auditRow?.getAttribute('data-state')).toBe('unknown')
  expect(screen.getByText('The audit log chain does not verify.')).toBeTruthy()
  expect(screen.getByText('No backup has been made yet.')).toBeTruthy()
})

test('keeps all notices when warning codes do not align with the warnings', async () => {
  await openSystem({
    'GET /v1/owner/system': () => system({
      warnings: ['First notice.', 'Second notice.'],
      warningCodes: ['backup'],
    }),
    'GET /v1/owner/system/diagnostics': () => report(),
  })
  expect(await screen.findByText('Backups')).toBeTruthy()
  expect(screen.getByText('First notice.')).toBeTruthy()
  expect(screen.getByText('Second notice.')).toBeTruthy()
})

test('renders hostile server strings as ordinary text', async () => {
  const hostile = '<img src=x onerror="window.__consolePwned = true">'
  await openSystem({
    'GET /v1/owner/system': () => system({
      warnings: [hostile],
      warningCodes: ['future'],
    }),
    'GET /v1/owner/system/diagnostics': () => report({
      checks: [{ id: 'backup', state: 'action', code: hostile, summary: hostile, next: hostile }],
      limitations: [hostile],
    }),
  })
  expect(await screen.findByText('Backups')).toBeTruthy()
  expect(screen.getAllByText(hostile).length).toBe(4)
  expect(document.querySelector('img[src="x"]')).toBeNull()
  expect((window as unknown as { __consolePwned?: boolean }).__consolePwned).toBeUndefined()
})
