import { cleanup, render } from '@testing-library/svelte'
import { afterEach, beforeEach, vi } from 'vitest'
import App from '../../src/App.svelte'
import { clearCSRFToken, onSessionEnded, onStepUpRequired, setAPIBase } from '../../src/lib/api'
import { endSession } from '../../src/lib/session.svelte'
import { cancelStepUp } from '../../src/lib/stepup.svelte'
import { device, installFakeApi, member, session, upToDate, type FakeRoute } from './fake-api'

export const config = { version: '0.1.0', setupRequired: false, apiBase: '' }

export const signedInRoutes: Record<string, FakeRoute> = {
  'GET /config.json': () => config,
  'GET /v1/owner/session': () => session,
  'GET /v1/owner/settings': () => ({ instanceId: 'inst-1', name: 'Rivera household', publicUrl: 'https://sesame.example.test', publicUrlSet: true, createdAt: '2026-07-01T00:00:00Z', fingerprint: 'ab12cd34' }),
  'GET /v1/owner/devices': () => ({ devices: [device] }),
  'GET /v1/owner/members': () => ({ members: [member] }),
  'GET /v1/owner/pairings': () => ({ pairings: [] }),
  'GET /v1/owner/owners': () => ({ owners: [{ id: 'own-1', name: 'Alex Rivera', createdAt: '2026-07-01T00:00:00Z', lastLoginAt: '2026-10-09T09:00:00Z', setupPending: false, current: true }] }),
  'GET /v1/owner/audit': () => ({ entries: [], nextCursor: 0, chain: { ok: true, rows: 0, headSeq: 0, headHash: '', firstBreak: null } }),
  'GET /v1/owner/system': () => ({ version: '0.1.0', commit: 'unknown', schemaVersion: 3, databaseBytes: 2048, lastBackupAt: null, warnings: [], auditChainOk: true, updateAvailable: false }),
  'GET /v1/owner/updates': () => upToDate,
}

export function openApp(url: string, routes: Record<string, FakeRoute> = {}) {
  window.history.replaceState(null, '', url)
  const fake = installFakeApi({ ...signedInRoutes, ...routes })
  render(App)
  return fake
}

export function useCleanApp() {
  beforeEach(() => {
    clearCSRFToken()
    setAPIBase('')
    window.localStorage.clear()
    window.sessionStorage.clear()
  })
  afterEach(() => {
    cancelStepUp()
    cleanup()
    endSession()
    onStepUpRequired(null)
    onSessionEnded(null)
    vi.useRealTimers()
    vi.unstubAllGlobals()
    window.history.replaceState(null, '', '/')
  })
}

export function storedText(): string {
  return JSON.stringify({ ...window.localStorage }) + JSON.stringify({ ...window.sessionStorage })
}
