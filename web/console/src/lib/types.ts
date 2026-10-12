export type Config = { version: string; setupRequired: boolean; apiBase: string }
export type OwnerRef = { id: string; name: string }
export type SessionInfo = { owner: OwnerRef; csrfToken: string; recentAuthUntil: string; expiresAt: string }
export type SetupDetails = { totpSecret: string; totpUri: string; ownerName: string; firstOwner: boolean; expiresAt: string }
export type Owner = { id: string; name: string; createdAt: string; lastLoginAt?: string | null; setupPending: boolean; current: boolean }
export type Member = { id: string; name: string; createdAt: string; deviceCount: number }
export type Holder = { kind: 'owner' | 'member'; id: string; name: string }
export type Device = {
  id: string
  name: string
  holder: Holder
  appVersion: string
  platform: string
  architecture: string
  createdAt: string
  expiresAt: string
  lastSeenAt: string
}
export type PendingPairing = { id: string; holder: Holder; deviceName: string; createdAt: string; expiresAt: string }
export type IssuedPairing = { pairingId: string; code: string; link: string; holder: Holder; expiresAt: string }
export type OwnerInvite = { owner: Owner; setupToken: string; link: string; expiresAt: string }
export type AuditEntry = {
  seq: number
  actor: string
  action: string
  target: string
  detail: Record<string, string>
  at: string
}
export type AuditChain = { ok: boolean; rows: number; firstBreak?: { seq: number; reason: string } | null }
export type AuditPage = { entries: AuditEntry[]; nextCursor: number; chain: AuditChain }
export type SystemInfo = {
  version: string
  commit: string
  schemaVersion: number
  databaseBytes: number
  lastBackupAt?: string | null
  warnings: string[]
  auditChainOk: boolean
  updateAvailable?: boolean
}
export type Settings = { instanceId: string; name: string; publicUrl: string; publicUrlSet: boolean; createdAt: string; fingerprint: string }
export type SettingsChange = { name?: string; publicUrl?: string }
export type MemberRemoval = { revokedDevices: number }
export type UpdateCommand = { label: string; text: string }
export type UpdateRelease = {
  version: string
  publishedAt: string
  notesUrl: string
  security: boolean
  minimumFrom: string
  images: { ref: string }[]
  binaries: { os: string; arch: string; url: string; sha256: string }[]
}
export type UpdateInfo = {
  configured: boolean
  enabled: boolean | null
  channel: string
  installKind: 'container' | 'binary'
  current: { product: string; version: string }
  latest: UpdateRelease | null
  available: boolean
  checkedAt: string | null
  error: string
  commands: UpdateCommand[]
}
export type UpdateChange = { enabled: boolean; channel: string }
