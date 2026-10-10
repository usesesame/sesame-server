import { mutate, request, setCSRFToken } from './api'
import type { AuditPage, Config, Device, IssuedPairing, Member, MemberRemoval, Owner, OwnerInvite, PendingPairing, SessionInfo, Settings, SettingsChange, SetupDetails, SystemInfo, UpdateChange, UpdateInfo } from './types'

export function loadConfig() {
  return request<Config>('/config.json')
}

export async function loadSession(): Promise<SessionInfo> {
  const session = await request<SessionInfo>('/v1/owner/session')
  setCSRFToken(session.csrfToken)
  return session
}

export const beginSetup = (token: string) => mutate<SetupDetails>('/v1/owner/setup/details', 'POST', { token }, { anonymous: true })
export const completeSetup = (input: { token: string; name: string; password: string; code: string; updateChecks?: boolean }) => mutate<unknown>('/v1/owner/setup', 'POST', input, { anonymous: true })
export const signIn = (input: { name: string; password: string; code: string }) => mutate<unknown>('/v1/owner/login', 'POST', input, { anonymous: true })
export const signOut = () => mutate<void>('/v1/owner/logout', 'POST')
export const stepUp = (input: { password: string; code: string }) => mutate<unknown>('/v1/owner/step-up', 'POST', input)

export async function listOwners() {
  return (await request<{ owners: Owner[] }>('/v1/owner/owners')).owners
}
export const inviteOwner = (name: string) => mutate<OwnerInvite>('/v1/owner/owners', 'POST', { name })
export const removeOwner = (id: string) => mutate<void>(`/v1/owner/owners/${encodeURIComponent(id)}`, 'DELETE')

export async function listMembers() {
  return (await request<{ members: Member[] }>('/v1/owner/members')).members
}
export const addMember = async (name: string) => (await mutate<{ member: Member }>('/v1/owner/members', 'POST', { name })).member
export const renameMember = async (id: string, name: string) => (await mutate<{ member: Member }>(`/v1/owner/members/${encodeURIComponent(id)}`, 'PATCH', { name })).member
export const removeMember = (id: string) => mutate<MemberRemoval>(`/v1/owner/members/${encodeURIComponent(id)}`, 'DELETE')

export type PairingTarget = { self: true } | { memberId: string }
export const createPairing = (target: PairingTarget, deviceName: string) =>
  mutate<IssuedPairing>('/v1/owner/pairings', 'POST', deviceName ? { ...target, deviceName } : target)
export async function listPairings() {
  return (await request<{ pairings: PendingPairing[] }>('/v1/owner/pairings')).pairings
}
export const cancelPairing = (id: string) => mutate<void>(`/v1/owner/pairings/${encodeURIComponent(id)}`, 'DELETE')

export async function listDevices() {
  return (await request<{ devices: Device[] }>('/v1/owner/devices')).devices
}
export const revokeDevice = (id: string) => mutate<void>(`/v1/owner/devices/${encodeURIComponent(id)}`, 'DELETE')

export function loadAudit(cursor?: number | null) {
  const query = cursor ? `?cursor=${encodeURIComponent(String(cursor))}` : ''
  return request<AuditPage>(`/v1/owner/audit${query}`)
}

export const loadSettings = () => request<Settings>('/v1/owner/settings')
export const saveSettings = (change: SettingsChange) => mutate<Settings>('/v1/owner/settings', 'PATCH', change)
export const loadSystem = () => request<SystemInfo>('/v1/owner/system')
export const exportData = () => mutate<unknown>('/v1/owner/export', 'POST', undefined, { timeoutMs: 60_000 })
export const loadUpdates = () => request<UpdateInfo>('/v1/owner/updates')
export const saveUpdates = (change: UpdateChange) => mutate<UpdateInfo>('/v1/owner/updates', 'PATCH', change)
export const checkUpdates = () => mutate<UpdateInfo>('/v1/owner/updates/check', 'POST', undefined, { timeoutMs: 20_000 })
