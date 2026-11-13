import type { SystemInfo } from './types'

export type DiagnosticsState = 'ok' | 'action' | 'unknown'

export type DiagnosticsCheck = {
  id: string
  label: string
  known: boolean
  valid: boolean
  state: DiagnosticsState
  code: string
  summary: string
  next: string | null
}

export type DiagnosticsReport = {
  generatedAt: string
  overall: DiagnosticsState
  checks: DiagnosticsCheck[]
  limitations: string[]
}

const CHECK_LABELS: Record<string, string> = {
  origin: 'Public address',
  proxy: 'Proxy trust',
  audit: 'Audit log',
  backup: 'Backups',
  forwarding: 'Forwarding',
  external: 'External checks',
}

const CHECK_CODES: Record<string, Record<string, readonly DiagnosticsState[]>> = {
  origin: { origin_https: ['ok'], origin_local: ['ok'] },
  proxy: { proxy_wide: ['action'], proxy_configured: ['ok'], proxy_not_configured: ['ok', 'unknown'] },
  audit: { audit_incremental_ok: ['ok'], audit_incremental_break: ['action'] },
  backup: {
    backup_missing: ['action'],
    backup_missing_schedule_off: ['action'],
    backup_future: ['unknown'],
    backup_recorded_schedule_off: ['unknown'],
    backup_stale: ['action'],
    backup_recorded: ['ok'],
  },
  forwarding: { forwarding_untrusted_observed: ['action'], forwarding_not_observed: ['ok'] },
  external: { external_not_inspected: ['unknown'] },
}

const MISSING_CHECK_SUMMARY = 'The server did not include this check in its report.'

const WARNING_CHECKS: Record<string, string> = {
  audit: 'audit',
  backup: 'backup',
  forwarding: 'forwarding',
  origin: 'origin',
}

const LIMITATION_LABELS: Record<string, string> = {
  certificate_validity_not_checked: 'Certificate validity was not checked.',
  network_reachability_not_probed: 'Network reachability was not probed.',
  host_configuration_not_inspected: 'Host configuration was not inspected.',
  audit_uses_incremental_verification: 'The audit check uses incremental verification, so it does not recheck every past entry.',
  backup_off_host_copy_not_verified: 'An off-host backup copy was not verified.',
  recovery_drill_not_verified: 'A recovery drill was not verified.',
  unseen_traffic_not_evaluated: 'Traffic the server has not seen was not evaluated.',
}

export const STATE_LABELS: Record<DiagnosticsState, string> = {
  ok: 'OK',
  action: 'Action needed',
  unknown: 'Unknown',
}

const STATE_RANK: Record<DiagnosticsState, number> = { ok: 0, unknown: 1, action: 2 }

const MAX_WARNINGS = 100
const MAX_WARNING_LENGTH = 500
const MAX_WARNING_CODE_LENGTH = 64
const MAX_CHECKS = 24
const MAX_LIMITATIONS = 32
const MAX_LIMITATION_LENGTH = 500
const MAX_ID_LENGTH = 64
const MAX_CODE_LENGTH = 80
const MAX_SUMMARY_LENGTH = 500
const MAX_GENERATED_AT_LENGTH = 64

function asRecord(value: unknown): Record<string, unknown> | null {
  return typeof value === 'object' && value !== null && !Array.isArray(value) ? value as Record<string, unknown> : null
}

function boundedText(value: unknown, maxLength: number): string | null {
  return typeof value === 'string' && value.length > 0 && value.length <= maxLength ? value : null
}

function normalizeState(value: string): DiagnosticsState {
  return value === 'ok' || value === 'action' || value === 'unknown' ? value : 'unknown'
}

function worseState(left: DiagnosticsState, right: DiagnosticsState): DiagnosticsState {
  return STATE_RANK[right] > STATE_RANK[left] ? right : left
}

export function parseDiagnosticsReport(value: unknown): DiagnosticsReport | null {
  const raw = asRecord(value)
  if (!raw || raw.schemaVersion !== 1) return null
  const generatedAt = boundedText(raw.generatedAt, MAX_GENERATED_AT_LENGTH)
  if (!generatedAt || Number.isNaN(Date.parse(generatedAt))) return null
  if (typeof raw.overall !== 'string') return null
  if (!Array.isArray(raw.checks) || raw.checks.length === 0 || raw.checks.length > MAX_CHECKS) return null
  if (!Array.isArray(raw.limitations) || raw.limitations.length > MAX_LIMITATIONS) return null

  const seen = new Set<string>()
  const checks: DiagnosticsCheck[] = []
  let overall = normalizeState(raw.overall)

  for (const item of raw.checks) {
    const entry = asRecord(item)
    if (!entry || typeof entry.state !== 'string') return null
    const id = boundedText(entry.id, MAX_ID_LENGTH)
    const code = boundedText(entry.code, MAX_CODE_LENGTH)
    const summary = boundedText(entry.summary, MAX_SUMMARY_LENGTH)
    if (!id || !code || !summary || seen.has(id)) return null
    seen.add(id)

    let next: string | null = null
    if (entry.next !== null && entry.next !== undefined) {
      if (typeof entry.next !== 'string') return null
      if (entry.next !== '') {
        next = boundedText(entry.next, MAX_SUMMARY_LENGTH)
        if (!next) return null
      }
    }

    const known = Object.hasOwn(CHECK_LABELS, id)
    const reportedState = normalizeState(entry.state)
    const codes = known ? CHECK_CODES[id] : undefined
    const valid = codes !== undefined && Object.hasOwn(codes, code) && (codes[code] as readonly string[]).includes(entry.state)
    const state: DiagnosticsState = valid || reportedState === 'action' ? reportedState : 'unknown'
    checks.push({
      id,
      label: known ? CHECK_LABELS[id] : 'Other check',
      known,
      valid,
      state,
      code,
      summary,
      next,
    })
    overall = worseState(overall, state)
  }

  for (const id of Object.keys(CHECK_LABELS)) {
    if (seen.has(id)) continue
    checks.push({ id, label: CHECK_LABELS[id], known: true, valid: false, state: 'unknown', code: 'missing', summary: MISSING_CHECK_SUMMARY, next: null })
    overall = worseState(overall, 'unknown')
  }

  const limitations = Object.keys(LIMITATION_LABELS)
  for (const item of raw.limitations) {
    const limitation = boundedText(item, MAX_LIMITATION_LENGTH)
    if (!limitation) return null
    if (!limitations.includes(limitation)) limitations.push(limitation)
  }

  return { generatedAt, overall, checks, limitations }
}

export function parseSystemInfo(value: unknown): SystemInfo {
  const raw = asRecord(value) ?? {}
  const warningEntries = Array.isArray(raw.warnings) ? raw.warnings : []
  const warnings: string[] = []
  let aligned = warningEntries.length <= MAX_WARNINGS && Array.isArray(raw.warningCodes)

  for (const entry of warningEntries.slice(0, MAX_WARNINGS)) {
    if (typeof entry !== 'string' || entry.length === 0) {
      aligned = false
      continue
    }
    if (entry.length > MAX_WARNING_LENGTH) {
      aligned = false
      warnings.push(`${entry.slice(0, MAX_WARNING_LENGTH - 1)}…`)
      continue
    }
    warnings.push(entry)
  }

  let warningCodes: string[] | undefined
  if (aligned && Array.isArray(raw.warningCodes) && raw.warningCodes.length === warnings.length) {
    const codes: string[] = []
    for (const entry of raw.warningCodes) {
      const code = boundedText(entry, MAX_WARNING_CODE_LENGTH)
      if (!code) {
        codes.length = 0
        break
      }
      codes.push(code)
    }
    if (codes.length === warnings.length) warningCodes = codes
  }

  const version = boundedText(raw.version, 64) ?? 'Unknown'
  const commit = boundedText(raw.commit, 64) ?? 'unknown'
  const schemaVersion = typeof raw.schemaVersion === 'number' && Number.isFinite(raw.schemaVersion) ? raw.schemaVersion : 0
  const databaseBytes = typeof raw.databaseBytes === 'number' && Number.isFinite(raw.databaseBytes) && raw.databaseBytes >= 0 ? raw.databaseBytes : Number.NaN
  const lastBackupAt = boundedText(raw.lastBackupAt, 64)

  return {
    version,
    commit,
    schemaVersion,
    databaseBytes,
    lastBackupAt,
    warnings,
    warningCodes,
    auditChainOk: raw.auditChainOk === true,
    updateAvailable: raw.updateAvailable === true ? true : undefined,
  }
}

export function visibleWarnings(info: SystemInfo, report: DiagnosticsReport | null): string[] {
  if (!report) return info.warnings
  const covered = new Set(report.checks.filter((check) => check.valid).map((check) => check.id))
  if (!info.warningCodes || info.warningCodes.length !== info.warnings.length) return info.warnings
  return info.warnings.filter((_, index) => {
    const code = info.warningCodes?.[index] ?? ''
    const checkId = Object.hasOwn(WARNING_CHECKS, code) ? WARNING_CHECKS[code] : undefined
    return checkId === undefined || !covered.has(checkId)
  })
}

export function limitationLabel(value: string): string {
  return Object.hasOwn(LIMITATION_LABELS, value) ? LIMITATION_LABELS[value] : value
}
