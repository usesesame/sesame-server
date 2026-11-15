import type { AccountActivityEvent } from './auth'

export type ActivityDescription = {
  title: string
  detail: string
}

const titles: Record<string, string> = {
  sign_in: 'Signed in with your password',
  password_changed: 'Changed the account password',
  password_set: 'Set an account password',
  password_recovered: 'Reset the password from an email link',
  email_changed: 'Changed the account email',
  desktop_linked: 'Connected a desktop',
  desktop_renamed: 'Renamed a desktop',
  desktop_revoked: 'Removed a desktop',
  passkey_added: 'Added a passkey',
  passkey_removed: 'Removed a passkey',
  passkey_clone_warning: 'Refused a passkey that may have been copied',
  download_ticket_issued: 'Created a download link',
  download_redeemed: 'Downloaded Sesame',
}

const genericLabels = new Set(['', 'Sesame account', 'Sesame desktop', 'Passkey', 'Browser', 'Verified private-beta download'])

export const knownActivityTypes = Object.keys(titles)

function fallbackTitle(type: string) {
  const words = type.replace(/[_-]+/g, ' ').trim()
  if (!words) return 'Account change'
  return `${words.charAt(0).toUpperCase()}${words.slice(1)}`
}

export function describeActivity(event: Pick<AccountActivityEvent, 'type' | 'label' | 'metadata'>): ActivityDescription {
  const title = event.type === 'sign_in' && event.metadata?.method === 'passkey'
    ? 'Signed in with a passkey'
    : titles[event.type] ?? fallbackTitle(event.type)
  const label = event.label?.trim() ?? ''
  const release = event.metadata?.release?.trim()
  const detail = event.type.startsWith('download_') && release
    ? `Release ${release}`
    : genericLabels.has(label) ? '' : label
  return { title, detail }
}
