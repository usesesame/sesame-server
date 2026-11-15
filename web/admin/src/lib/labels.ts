import type { AuditEntry, Release, Role } from './types'

export const ROLE_LABELS: Record<Role, string> = {
  super: 'Super admin',
  support: 'Support',
  ops: 'Operations',
  billing: 'Billing',
  readonly: 'Read only',
}

export function roleLabel(role: string): string {
  return Object.hasOwn(ROLE_LABELS, role) ? ROLE_LABELS[role as Role] : sentenceCase(role)
}

export function sentenceCase(value: string): string {
  const words = value.replaceAll('_', ' ').replaceAll('-', ' ').trim()
  return words ? words.charAt(0).toUpperCase() + words.slice(1) : ''
}

export const REGISTRATION_LABELS: Record<string, string> = {
  closed: 'Closed',
  invite: 'Invitation only',
  public: 'Public',
}

const REGISTRATION_EFFECTS: Record<string, string> = {
  closed: 'No one can create a website account.',
  invite: 'Only people with an invitation can create a website account.',
  public: 'Anyone can create a website account.',
}

export function registrationEffect(value: string): string {
  return REGISTRATION_EFFECTS[value] ?? ''
}

type FlagCopy = { label: string; effect: string }

const FLAG_COPY: Record<string, FlagCopy> = {
  registration_mode: { label: 'Registration', effect: 'Decides who can create a website account.' },
  cloud_sync_available: { label: 'Sesame Sync', effect: 'Lets accounts use Sync when the server has Sync configured.' },
  public_download: { label: 'Public download', effect: 'Offers the latest published build on the website without a beta account.' },
  desktop_linking_enabled: { label: 'Desktop linking', effect: 'Lets people link a desktop app to their website account.' },
  downloads_enabled: { label: 'Beta downloads', effect: 'Lets beta accounts download verified builds from their account.' },
  updater_enabled: { label: 'Desktop updates', effect: 'Lets desktop apps ask this server for updates.' },
  browser_extension_visible: { label: 'Browser extension on the website', effect: 'Shows the Chrome Web Store link on the website.' },
}

export function flagCopy(key: string): FlagCopy {
  return Object.hasOwn(FLAG_COPY, key) ? FLAG_COPY[key] : { label: sentenceCase(key), effect: '' }
}

export function auditActor(entry: Pick<AuditEntry, 'adminEmail' | 'action'>): string {
  if (entry.adminEmail) return entry.adminEmail
  return entry.action.startsWith('release.') ? 'Release pipeline' : 'System'
}

const PLATFORM_LABELS: Record<string, string> = { linux: 'Linux', windows: 'Windows' }

export function releaseAudience(release: Pick<Release, 'platform' | 'channel'>): string {
  return `${PLATFORM_LABELS[release.platform] ?? sentenceCase(release.platform)} devices on the ${release.channel} channel`
}

export function formatCount(value: number): string {
  return new Intl.NumberFormat().format(value)
}
