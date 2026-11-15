import { expect, test } from 'vitest'
import { stepUpAction } from '../src/lib/stepup-action'

test.each([
  [{ method: 'POST', path: '/v1/owner/owners', body: JSON.stringify({ name: 'Riley Chen' }) }, 'invite Riley Chen as an owner'],
  [{ method: 'DELETE', path: '/v1/owner/owners/own-2' }, 'remove an owner'],
  [{ method: 'DELETE', path: '/v1/owner/members/mem-1' }, 'remove a member and revoke their devices'],
  [{ method: 'DELETE', path: '/v1/owner/devices/dev-1' }, 'revoke a device'],
  [{ method: 'PATCH', path: '/v1/owner/flags/sync_enabled', body: JSON.stringify({ enabled: true }) }, 'turn on Sync'],
  [{ method: 'PATCH', path: '/v1/owner/flags/sync_enabled', body: JSON.stringify({ enabled: false }) }, 'turn off Sync for every device'],
  [{ method: 'DELETE', path: '/v1/owner/sync/member/mem-1' }, 'reset a synced vault'],
  [{ method: 'PATCH', path: '/v1/owner/settings', body: '{}' }, 'save the settings'],
])('names the action for %o', (request, expected) => {
  expect(stepUpAction(request)).toBe(expected)
})

test('falls back to a generic phrase for malformed bodies and unknown routes', () => {
  expect(stepUpAction({ method: 'POST', path: '/v1/owner/owners', body: '{not json' })).toBe('invite an owner')
  expect(stepUpAction({ method: 'POST', path: '/v1/owner/owners', body: JSON.stringify({ name: 'x'.repeat(65) }) })).toBe('invite an owner')
  expect(stepUpAction({ method: 'POST', path: '/v1/owner/owners', body: JSON.stringify({ name: 42 }) })).toBe('invite an owner')
  expect(stepUpAction({ method: 'POST', path: '/v1/owner/something-new' })).toBe('continue')
})
