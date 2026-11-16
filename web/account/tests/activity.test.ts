import { readFileSync, readdirSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { expect, test } from 'vitest'
import { describeActivity, knownActivityTypes } from '../src/lib/activity'

const serverRoutes = resolve(process.cwd(), '../../internal/httpapi')

function recordedEventTypes() {
  const types = new Set<string>()
  for (const name of readdirSync(serverRoutes)) {
    if (!name.endsWith('.go') || name.endsWith('_test.go')) continue
    const source = readFileSync(join(serverRoutes, name), 'utf8')
    for (const match of source.matchAll(/recordAccountEvent\([^,]+,[^,]+,\s*"([a-z_]+)"/g)) types.add(match[1])
  }
  return [...types].sort()
}

test('every account event type the server records has its own sentence', () => {
  const recorded = recordedEventTypes()
  expect(recorded.length).toBeGreaterThanOrEqual(13)
  expect(recorded.filter((type) => !knownActivityTypes.includes(type))).toEqual([])
})

test('a passkey sign-in and a password sign-in read differently', () => {
  expect(describeActivity({ type: 'sign_in', label: 'Passkey', metadata: { method: 'passkey' } })).toEqual({ title: 'Signed in with a passkey', detail: '' })
  expect(describeActivity({ type: 'sign_in', label: 'Firefox on Linux' })).toEqual({ title: 'Signed in with your password', detail: 'Firefox on Linux' })
})

test('the label names the desktop or passkey and generic labels are left out', () => {
  expect(describeActivity({ type: 'desktop_revoked', label: 'Sesame desktop' })).toEqual({ title: 'Removed a desktop', detail: '' })
  expect(describeActivity({ type: 'desktop_linked', label: 'Studio desktop' })).toEqual({ title: 'Connected a desktop', detail: 'Studio desktop' })
  expect(describeActivity({ type: 'email_changed', label: 'Sesame account' })).toEqual({ title: 'Changed the account email', detail: '' })
  expect(describeActivity({ type: 'passkey_added', label: 'Work laptop' })).toEqual({ title: 'Added a passkey', detail: 'Work laptop' })
  expect(describeActivity({ type: 'sync_vault_deleted', label: 'Sesame Sync' })).toEqual({ title: 'Deleted the synced vault', detail: '' })
})

test('download events name the release instead of the generic label', () => {
  expect(describeActivity({ type: 'download_ticket_issued', label: 'Verified private-beta download', metadata: { release: '0.4.1', platform: 'linux' } }))
    .toEqual({ title: 'Created a download link', detail: 'Release 0.4.1' })
})

test('an unknown event type still reads as a sentence', () => {
  expect(describeActivity({ type: 'recovery_codes_viewed', label: 'Sesame account' })).toEqual({ title: 'Recovery codes viewed', detail: '' })
  expect(describeActivity({ type: '', label: '' })).toEqual({ title: 'Account change', detail: '' })
})
