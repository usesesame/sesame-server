export type Role = 'super' | 'support' | 'ops' | 'billing' | 'readonly'
export type AdminAccount = { id: string; email: string; role: Role; mfaVerified: boolean; suspended: boolean; createdAt: string; lastLoginAt?: string }
export type User = { id: string; email: string; emailVerified: boolean; betaAccess: boolean; suspendedAt?: string; suspendedReason?: string; createdAt: string; sessionCount: number; deviceCount: number; sessions?: Session[]; devices?: Device[] }
export type Session = { id: string; label: string; createdAt: string; lastSeenAt: string; expiresAt: string }
export type Device = { id: string; name: string; connectedAt: string; expiresAt: string }
export type Flag = { key: string; value: string; updatedAt: string }
export type Plan = { id: string; name: string; price: string; annualPrice?: string; billing: 'none' | 'one_time' | 'monthly' | 'yearly'; description: string; available: boolean; includes: string[] }
export type ReleaseArtifact = { id: string; url: string; sha256: string; bytes: number; updaterSignature: string; updaterSigningKeyId: string; distributionClass: 'lab' | 'early_access' | 'production'; sigstoreVerified: boolean; sigstoreIssuer?: string; sigstoreIdentity?: string; sigstoreBundleSha256?: string; authenticodeVerified: boolean; authenticodeSubject?: string; authenticodeThumbprint?: string; verifiedAt: string }
export type Release = { id: string; channel: string; platform: string; architecture: string; version: string; url: string; sha256: string; signature: string; signingKeyId: string; supportedWindows: string; releaseNotesUrl: string; rollbackNotice: string; status: 'draft' | 'published' | 'withdrawn'; rolloutPercent: number; updateEnabled: boolean; killSwitch: boolean; manifestRevision: number; publishedAt?: string; artifact?: ReleaseArtifact }
export type AuditEntry = { id: number; adminEmail: string; action: string; targetType: string; targetId?: string; detail: Record<string, unknown>; createdAt: string }
export type Overview = { users: number; newUsersThisWeek: number; betaUsers: number; unverifiedUsers: number; suspendedUsers: number; activeAdminSessions: number; openTickets: number; unassignedTickets: number; urgentTickets: number }
export type RateMetric = { operation: string; buckets: number; attempts: number; updatedAt: string }

export type TicketStatus = 'open' | 'in_progress' | 'waiting' | 'closed'
export type TicketPriority = 'low' | 'normal' | 'high' | 'urgent'
export type TicketCategory = 'general' | 'account' | 'import' | 'sync' | 'browser_helper' | 'billing' | 'bug'

export const TICKET_CATEGORY_LABELS: Record<TicketCategory, string> = {
  general: 'General',
  account: 'Account & sign-in',
  import: 'Import & migration',
  sync: 'Sesame Sync',
  browser_helper: 'Browser helper',
  billing: 'Billing & licences',
  bug: 'Bug report',
}

export type TicketSummary = {
  id: string; email: string; subject: string; status: TicketStatus
  priority: TicketPriority; category: TicketCategory; appVersion: string; diagnosticCode: string; browserIntegration: string; requestId: string
  assignedAdminId?: string; messageCount: number
  createdAt: string; updatedAt: string; firstResponseAt?: string; closedAt?: string; slaDueAt: string; slaBreached: boolean; queuePosition?: number
}

export type TicketMessage = {
  id: string; authorRole: 'user' | 'staff'; adminEmail?: string
  body: string; sentViaEmail: boolean; emailDeliveryStatus?: string; emailAttempts?: number; emailNextAttemptAt?: string; createdAt: string
}

export type TicketNote = {
  id: string; adminEmail: string; body: string; createdAt: string
}

export type TicketDetail = TicketSummary & {
  accountId?: string; linkedDevices?: Device[]; messages: TicketMessage[]; notes: TicketNote[]
}
