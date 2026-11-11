import { expect, test } from 'vitest'
import { limitationLabel, parseDiagnosticsReport, parseSystemInfo, visibleWarnings } from '../src/lib/system'

const check = (overrides: Record<string, unknown> = {}) => ({
  id: 'origin',
  state: 'ok',
  code: 'origin_local',
  summary: 'The public address is a loopback HTTP origin.',
  next: null,
  ...overrides,
})

const report = (overrides: Record<string, unknown> = {}) => ({
  schemaVersion: 1,
  generatedAt: '2026-11-08T10:00:00Z',
  overall: 'ok',
  checks: [check()],
  limitations: [],
  ...overrides,
})

test('parses a valid report and gives action precedence over unknown and ok', () => {
  const parsed = parseDiagnosticsReport(report({
    overall: 'unknown',
    checks: [
      check(),
      check({ id: 'proxy', state: 'unknown', code: 'proxy_not_configured', summary: 'No trusted proxy ranges are configured.' }),
      check({ id: 'backup', state: 'action', code: 'backup_missing', summary: 'No backup has been recorded.' }),
      check({ id: 'external', state: 'unknown', code: 'external_not_inspected', summary: 'External checks were not run.' }),
    ],
  }))
  expect(parsed).not.toBeNull()
  expect(parsed?.overall).toBe('action')
  expect(parsed?.checks.map((item) => [item.id, item.label, item.state, item.valid])).toEqual([
    ['origin', 'Public address', 'ok', true],
    ['proxy', 'Proxy trust', 'unknown', true],
    ['backup', 'Backups', 'action', true],
    ['external', 'External checks', 'unknown', true],
    ['audit', 'Audit log', 'unknown', false],
    ['forwarding', 'Forwarding', 'unknown', false],
  ])
})

const completeChecks = () => [
  check({ id: 'origin', state: 'ok', code: 'origin_https', summary: 'The configured public address uses HTTPS.' }),
  check({ id: 'proxy', state: 'ok', code: 'proxy_configured', summary: 'Trusted proxy ranges are configured.' }),
  check({ id: 'audit', state: 'ok', code: 'audit_incremental_ok', summary: 'The incremental audit verification found no break.' }),
  check({ id: 'backup', state: 'ok', code: 'backup_recorded', summary: 'The last recorded backup is recent.' }),
  check({ id: 'forwarding', state: 'ok', code: 'forwarding_not_observed', summary: 'No untrusted forwarding was observed.' }),
]

test('a report that leaves out a required check cannot be ok', () => {
  const partial = parseDiagnosticsReport(report({ overall: 'ok', checks: [check({ state: 'ok', code: 'origin_https', summary: 'The configured public address uses HTTPS.' })] }))
  expect(partial?.overall).toBe('unknown')
  expect(partial?.checks.map((item) => item.id)).toEqual(['origin', 'proxy', 'audit', 'backup', 'forwarding', 'external'])
  expect(partial?.checks.slice(1).every((item) => item.state === 'unknown' && !item.valid)).toBe(true)

  const withoutExternal = parseDiagnosticsReport(report({ overall: 'ok', checks: completeChecks() }))
  expect(withoutExternal?.overall).toBe('unknown')
  expect(withoutExternal?.checks.at(-1)).toMatchObject({ id: 'external', state: 'unknown', valid: false })
})

test('a state that contradicts its code cannot be ok', () => {
  const parsed = parseDiagnosticsReport(report({
    overall: 'ok',
    checks: [
      ...completeChecks().filter((item) => item.id !== 'backup'),
      check({ id: 'backup', state: 'ok', code: 'backup_missing', summary: 'No backup is recorded.' }),
      check({ id: 'external', state: 'ok', code: 'external_not_inspected', summary: 'External checks were not run.' }),
    ],
  }))
  expect(parsed?.overall).toBe('unknown')
  expect(parsed?.checks.find((item) => item.id === 'backup')).toMatchObject({ state: 'unknown', valid: false })
  expect(parsed?.checks.find((item) => item.id === 'external')).toMatchObject({ state: 'unknown', valid: false })
})

test('always lists the fixed limitations and keeps extra ones once', () => {
  const parsed = parseDiagnosticsReport(report({ limitations: ['recovery_drill_not_verified', 'A limitation from a later server.', 'A limitation from a later server.'] }))
  expect(parsed?.limitations).toEqual([
    'certificate_validity_not_checked',
    'network_reachability_not_probed',
    'host_configuration_not_inspected',
    'audit_uses_incremental_verification',
    'backup_off_host_copy_not_verified',
    'recovery_drill_not_verified',
    'unseen_traffic_not_evaluated',
    'A limitation from a later server.',
  ])
})

test('treats an unknown future check and state as unknown', () => {
  const parsed = parseDiagnosticsReport(report({
    checks: [
      check({ id: 'future_check', state: 'ok', code: 'future_ok', summary: 'A future check reported success.' }),
      check({ id: 'audit', state: 'review', code: 'audit_review', summary: 'A future audit state was reported.' }),
    ],
  }))
  expect(parsed?.overall).toBe('unknown')
  expect(parsed?.checks[0]).toMatchObject({ label: 'Other check', state: 'unknown', known: false })
  expect(parsed?.checks[1]).toMatchObject({ label: 'Audit log', state: 'unknown', known: true })
})

test('does not turn an unknown code into ok but keeps an action', () => {
  const parsed = parseDiagnosticsReport(report({
    checks: [
      check({ id: 'origin', state: 'ok', code: 'origin_future', summary: 'A future origin code reported success.' }),
      check({ id: 'backup', state: 'action', code: 'backup_future_rule', summary: 'A future backup code needs action.' }),
    ],
  }))
  expect(parsed?.checks[0]).toMatchObject({ label: 'Public address', state: 'unknown', known: true })
  expect(parsed?.checks[1]).toMatchObject({ label: 'Backups', state: 'action', known: true })
  expect(parsed?.overall).toBe('action')
})

test('rejects unsupported and malformed reports', () => {
  expect(parseDiagnosticsReport(report({ schemaVersion: 2 }))).toBeNull()
  expect(parseDiagnosticsReport(report({ generatedAt: 'not-a-date' }))).toBeNull()
  expect(parseDiagnosticsReport(report({ generatedAt: '' }))).toBeNull()
  expect(parseDiagnosticsReport(report({ checks: [] }))).toBeNull()
  expect(parseDiagnosticsReport(report({ checks: [check({ id: 'origin' }), check({ id: 'origin' })] }))).toBeNull()
  expect(parseDiagnosticsReport(report({ checks: [check({ state: 1 })] }))).toBeNull()
  expect(parseDiagnosticsReport(report({ checks: [check({ summary: 'x'.repeat(501) })] }))).toBeNull()
  expect(parseDiagnosticsReport(report({ checks: [check({ next: 4 })] }))).toBeNull()
  expect(parseDiagnosticsReport(report({ limitations: ['x'.repeat(501)] }))).toBeNull()
  expect(parseDiagnosticsReport(report({ limitations: 'none' }))).toBeNull()
  expect(parseDiagnosticsReport(report({ checks: Array.from({ length: 25 }, () => check()) }))).toBeNull()
  expect(parseDiagnosticsReport(report({ limitations: Array.from({ length: 33 }, () => 'network_not_probed') }))).toBeNull()
})

test('bounds system warnings and keeps warning codes aligned', () => {
  const info = parseSystemInfo({
    version: '0.1.0',
    commit: 'abc',
    schemaVersion: 3,
    databaseBytes: 2048,
    lastBackupAt: null,
    warnings: ['No backup has been made yet.', 'A configuration warning.'],
    warningCodes: ['backup', 'configuration'],
    auditChainOk: true,
  })
  expect(info.warnings).toEqual(['No backup has been made yet.', 'A configuration warning.'])
  expect(info.warningCodes).toEqual(['backup', 'configuration'])
  expect(info.auditChainOk).toBe(true)
})

test('drops warning codes when the arrays do not align', () => {
  const info = parseSystemInfo({ warnings: ['One.', 'Two.'], warningCodes: ['backup'] })
  expect(info.warningCodes).toBeUndefined()
  expect(info.warnings).toEqual(['One.', 'Two.'])
})

test('drops warning codes when a warning cannot be read', () => {
  const info = parseSystemInfo({ warnings: ['One.', 7], warningCodes: ['backup', 'audit'] })
  expect(info.warnings).toEqual(['One.'])
  expect(info.warningCodes).toBeUndefined()
})

test('bounds warnings and uses safe fact fallbacks', () => {
  const info = parseSystemInfo({
    version: '',
    commit: 'x'.repeat(65),
    schemaVersion: 'three',
    databaseBytes: -2,
    warnings: Array.from({ length: 101 }, (_, index) => `Notice ${index}.`),
    warningCodes: Array.from({ length: 101 }, () => 'backup'),
    auditChainOk: 'yes',
  })
  expect(info.version).toBe('Unknown')
  expect(info.commit).toBe('unknown')
  expect(info.schemaVersion).toBe(0)
  expect(Number.isNaN(info.databaseBytes)).toBe(true)
  expect(info.warnings).toHaveLength(100)
  expect(info.warningCodes).toBeUndefined()
  expect(info.auditChainOk).toBe(false)
})

test('hides only coded notices that a valid report covers', () => {
  const info = parseSystemInfo({
    warnings: ['Backup notice.', 'Configuration notice.', 'Future notice.', 'Origin notice.'],
    warningCodes: ['backup', 'configuration', 'future', 'origin'],
  })
  const parsed = parseDiagnosticsReport(report({
    checks: [check({ id: 'backup', state: 'action', code: 'backup_missing', summary: 'No backup has been recorded.' })],
  }))
  expect(visibleWarnings(info, parsed)).toEqual(['Configuration notice.', 'Future notice.', 'Origin notice.'])
  expect(visibleWarnings(info, null)).toEqual(info.warnings)
})

test('keeps a notice when its report check is missing or the arrays do not align', () => {
  const info = parseSystemInfo({
    warnings: ['Backup notice.', 'Audit notice.'],
    warningCodes: ['backup', 'audit'],
  })
  const parsed = parseDiagnosticsReport(report({ checks: [check({ id: 'origin' })] }))
  expect(visibleWarnings(info, parsed)).toEqual(['Backup notice.', 'Audit notice.'])

  const unrecognized = parseDiagnosticsReport(report({ checks: [check({ id: 'audit', state: 'action', code: 'audit_future_rule', summary: 'A future audit code.' })] }))
  expect(visibleWarnings(info, unrecognized)).toEqual(['Backup notice.', 'Audit notice.'])

  const contradicted = parseDiagnosticsReport(report({ checks: [check({ id: 'backup', state: 'ok', code: 'backup_missing', summary: 'No backup is recorded.' })] }))
  expect(visibleWarnings(info, contradicted)).toEqual(['Backup notice.', 'Audit notice.'])

  const misaligned = parseSystemInfo({ warnings: ['Backup notice.', 'Audit notice.'], warningCodes: ['backup'] })
  expect(visibleWarnings(misaligned, parsed)).toEqual(['Backup notice.', 'Audit notice.'])
})

test('maps known limitation codes and preserves unknown text', () => {
  expect(limitationLabel('network_not_probed')).toBe('network_not_probed')
  expect(limitationLabel('network_reachability_not_probed')).toBe('Network reachability was not probed.')
  expect(limitationLabel('A limitation from a later server.')).toBe('A limitation from a later server.')
  expect(limitationLabel('__proto__')).toBe('__proto__')
  expect(limitationLabel('constructor')).toBe('constructor')
})
