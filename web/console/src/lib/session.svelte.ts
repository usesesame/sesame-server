import { APIError, clearCSRFToken } from './api'
import { loadSession, loadSettings, signOut as requestSignOut } from './owner'
import type { OwnerRef } from './types'

export const session = $state<{ owner: OwnerRef | null; instanceName: string; notice: string }>({ owner: null, instanceName: '', notice: '' })

export type SessionResult = { status: 'signed-in' } | { status: 'signed-out' } | { status: 'unavailable'; message: string }

export async function establishSession(): Promise<SessionResult> {
  try {
    const info = await loadSession()
    session.owner = info.owner
  } catch (reason) {
    session.owner = null
    if (reason instanceof APIError && reason.status === 401) return { status: 'signed-out' }
    return { status: 'unavailable', message: reason instanceof Error ? reason.message : 'The server did not answer.' }
  }
  try {
    session.instanceName = (await loadSettings()).name
  } catch {
    session.instanceName = ''
  }
  return { status: 'signed-in' }
}

export function endSession(notice = '') {
  clearCSRFToken()
  session.owner = null
  session.instanceName = ''
  session.notice = notice
}

export async function signOutOwner() {
  try {
    await requestSignOut()
  } finally {
    endSession()
  }
}
