export type StepUpRequest = { method: string; path: string; body?: BodyInit | null }

const MAX_NAME_LENGTH = 64

function bodyField(body: StepUpRequest['body'], field: string): unknown {
  if (typeof body !== 'string') return undefined
  try {
    const parsed: unknown = JSON.parse(body)
    return typeof parsed === 'object' && parsed !== null ? (parsed as Record<string, unknown>)[field] : undefined
  } catch {
    return undefined
  }
}

function nameIn(body: StepUpRequest['body']): string {
  const value = bodyField(body, 'name')
  return typeof value === 'string' && value.trim() !== '' && value.length <= MAX_NAME_LENGTH ? value.trim() : ''
}

export function stepUpAction(request: StepUpRequest): string {
  const method = request.method.toUpperCase()
  const path = request.path.split('?')[0]
  const name = nameIn(request.body)
  if (path === '/v1/owner/owners' && method === 'POST') return name ? `invite ${name} as an owner` : 'invite an owner'
  if (path.startsWith('/v1/owner/owners/') && method === 'DELETE') return 'remove an owner'
  if (path === '/v1/owner/members' && method === 'POST') return name ? `add ${name} as a member` : 'add a member'
  if (path.startsWith('/v1/owner/members/') && method === 'PATCH') return name ? `rename a member to ${name}` : 'rename a member'
  if (path.startsWith('/v1/owner/members/') && method === 'DELETE') return 'remove a member and revoke their devices'
  if (path === '/v1/owner/pairings' && method === 'POST') return 'create a pairing code'
  if (path.startsWith('/v1/owner/pairings/') && method === 'DELETE') return 'cancel a pairing code'
  if (path.startsWith('/v1/owner/devices/') && method === 'DELETE') return 'revoke a device'
  if (path === '/v1/owner/flags/sync_enabled' && method === 'PATCH') return bodyField(request.body, 'enabled') === true ? 'turn on Sync' : 'turn off Sync for every device'
  if (path.startsWith('/v1/owner/sync/') && method === 'DELETE') return 'reset a synced vault'
  if (path === '/v1/owner/settings' && method === 'PATCH') return 'save the settings'
  if (path === '/v1/owner/export' && method === 'POST') return 'export the server data'
  if (path === '/v1/owner/updates' && method === 'PATCH') return 'change the update settings'
  if (path === '/v1/owner/updates/check' && method === 'POST') return 'check for updates'
  return 'continue'
}
