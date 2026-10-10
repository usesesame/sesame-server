import { createServer } from 'node:http'
import { randomBytes } from 'node:crypto'
import { readFile, stat } from 'node:fs/promises'
import { extname, join, normalize } from 'node:path'
import { CONSOLE_CSP } from '../scripts/csp.mjs'

const TYPES = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.woff2': 'font/woff2',
}
const SETUP_TOKEN = 'setup-token-for-screenshots-0000000000'
const TOTP_SECRET = 'JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP'
const GOOD_CODE = '123456'
const FINGERPRINT = '9f2c41d0a7b35e68c1d4f0a9e2b7835c6d1f4a08b9e3c27d5a6f1e0b4c8d9a73'
const minutes = (count) => new Date(Date.now() + count * 60_000).toISOString().replace(/\.\d+Z$/, 'Z')

const UPDATE_SCENARIOS = {
  current: { enabled: true, latest: '0.1.0', security: false, available: false, checked: true, error: '' },
  available: { enabled: true, latest: '0.2.0', security: true, available: true, checked: true, error: '' },
  unset: { enabled: null, latest: null, security: false, available: false, checked: false, error: '' },
  error: { enabled: true, latest: null, security: false, available: false, checked: false, error: 'feed_unreachable' },
  unconfigured: { configured: false, enabled: null, latest: null, security: false, available: false, checked: false, error: 'not_configured' },
}

function updateView(scenario) {
  const latest = scenario.latest ? { version: scenario.latest, publishedAt: minutes(-60 * 24 * 3), notesUrl: `https://updates.example.test/notes/${scenario.latest}`, security: scenario.security, minimumFrom: '', images: [{ ref: `registry.example.test/sesame/server:${scenario.latest}` }], binaries: [{ os: 'linux', arch: 'amd64', url: `https://updates.example.test/sesame-server-${scenario.latest}-linux-amd64`, sha256: '0'.repeat(64) }] } : null
  return {
    configured: scenario.configured ?? true,
    enabled: scenario.enabled,
    channel: 'stable',
    installKind: 'container',
    current: { product: 'sesame-server', version: '0.1.0' },
    latest,
    available: scenario.available,
    checkedAt: scenario.checked ? minutes(-12) : null,
    error: scenario.error,
    commands: scenario.available ? [{ label: 'Set the version', text: `SESAME_IMAGE_TAG=${scenario.latest}` }, { label: 'Pull and restart', text: 'docker compose pull && docker compose up -d' }] : [],
  }
}

function seed(options) {
  const members = [
    { id: 'mem-1', name: 'Priya Raman', createdAt: minutes(-60 * 24 * 40), deviceCount: 2 },
    { id: 'mem-2', name: 'Tomas Berg', createdAt: minutes(-60 * 24 * 12), deviceCount: 1 },
    { id: 'mem-3', name: 'Guest laptop', createdAt: minutes(-60 * 24 * 2), deviceCount: 0 },
  ]
  const holder = (kind, id, name) => ({ kind, id, name })
  const devices = [
    { id: 'dev-1', name: 'Studio desktop', holder: holder('owner', 'own-1', 'Alex Rivera'), appVersion: '0.3.1', platform: 'Windows 11', architecture: 'x86_64', createdAt: minutes(-60 * 24 * 60), expiresAt: minutes(60 * 24 * 30), lastSeenAt: minutes(-3) },
    { id: 'dev-2', name: 'Priya laptop', holder: holder('member', 'mem-1', 'Priya Raman'), appVersion: '0.3.1', platform: 'Arch Linux', architecture: 'x86_64', createdAt: minutes(-60 * 24 * 35), expiresAt: minutes(60 * 24 * 55), lastSeenAt: minutes(-60 * 5) },
    { id: 'dev-3', name: 'Priya work PC', holder: holder('member', 'mem-1', 'Priya Raman'), appVersion: '0.3.0', platform: 'Windows 10', architecture: 'x86_64', createdAt: minutes(-60 * 24 * 30), expiresAt: minutes(60 * 24 * 60), lastSeenAt: minutes(-60 * 24 * 9) },
    { id: 'dev-4', name: 'Tomas desktop', holder: holder('member', 'mem-2', 'Tomas Berg'), appVersion: '0.3.1', platform: 'Ubuntu 24.04', architecture: 'aarch64', createdAt: minutes(-60 * 24 * 11), expiresAt: minutes(60 * 24 * 79), lastSeenAt: minutes(-40) },
  ]
  const owners = [
    { id: 'own-1', name: 'Alex Rivera', createdAt: minutes(-60 * 24 * 90), lastLoginAt: minutes(-12), setupPending: false, current: true },
    { id: 'own-2', name: 'Sam Okafor', createdAt: minutes(-60 * 24 * 20), lastLoginAt: minutes(-60 * 30), setupPending: false, current: false },
    { id: 'own-3', name: 'Jo Tanaka', createdAt: minutes(-60), lastLoginAt: null, setupPending: true, current: false },
  ]
  const actions = ['owner.login', 'pairing.created', 'device.linked', 'member.created', 'device.revoked', 'settings.updated', 'export.created']
  const audit = Array.from({ length: options.auditRows ?? 80 }, (_, index) => {
    const seq = (options.auditRows ?? 80) - index
    return { seq, actor: seq % 5 === 0 ? 'device:dev-2' : 'owner:own-1', action: actions[seq % actions.length], target: seq % 3 === 0 ? 'member:mem-1' : 'instance', detail: seq % 4 === 0 ? { device: 'Priya laptop' } : {}, at: minutes(-seq * 37), hash: randomBytes(32).toString('hex') }
  })
  return {
    setupRequired: options.setupRequired ?? false,
    instance: { instanceId: 'inst-1', name: 'Rivera household', publicUrl: options.origin, publicUrlSet: true, createdAt: minutes(-60 * 24 * 90), fingerprint: FINGERPRINT },
    members, devices, owners, audit, pairings: [], sessions: new Map(),
    chainBroken: options.chainBroken ?? false,
    warnings: options.warnings ?? ['No backup has been made yet.'],
    lastBackupAt: options.lastBackupAt === undefined ? minutes(-60 * 14) : options.lastBackupAt,
    updates: { ...UPDATE_SCENARIOS[options.updates ?? 'current'] },
    recentUntil: 0,
    requests: [],
  }
}

export async function startMock(options = {}) {
  const dist = options.dist
  const port = options.port ?? 0
  let origin = ''
  const state = seed({ ...options, origin: '' })

  const send = (response, status, body, headers = {}) => {
    const text = body === undefined ? '' : JSON.stringify(body)
    response.writeHead(status, { 'Content-Type': 'application/json; charset=utf-8', 'Cache-Control': 'no-store', 'Content-Security-Policy': "default-src 'none'", ...headers })
    response.end(text)
  }
  const fail = (response, status, code, message) => send(response, status, { error: { code, message } })
  const readBody = async (request) => {
    const chunks = []
    for await (const chunk of request) chunks.push(chunk)
    const text = Buffer.concat(chunks).toString('utf8')
    return text ? JSON.parse(text) : {}
  }
  const cookieOf = (request) => /(?:^|;\s*)sesame_owner=([^;]+)/.exec(request.headers.cookie ?? '')?.[1]
  const sessionView = (session) => ({ owner: { id: 'own-1', name: 'Alex Rivera' }, csrfToken: session.csrf, recentAuthUntil: new Date(state.recentUntil).toISOString(), expiresAt: minutes(60 * 12) })
  const startSession = (response) => {
    const token = randomBytes(24).toString('hex')
    const session = { csrf: randomBytes(16).toString('hex') }
    state.sessions.set(token, session)
    state.recentUntil = Date.now() + 10 * 60_000
    response.setHeader('Set-Cookie', `sesame_owner=${token}; Path=/; HttpOnly; SameSite=Strict`)
    return session
  }

  const handler = async (request, response) => {
    const url = new URL(request.url, 'http://mock')
    const path = url.pathname
    const method = request.method
    if (path === '/config.json') return send(response, 200, { version: '0.1.0', setupRequired: state.setupRequired, apiBase: '' })
    if (!path.startsWith('/v1/')) return serveStatic(response, path)
    state.requests.push(`${method} ${path}`)
    if (options.delayMs) await new Promise((resolve) => setTimeout(resolve, options.delayMs))
    let body = {}
    try {
      body = ['POST', 'PATCH', 'PUT'].includes(method) ? await readBody(request) : {}
    } catch {
      return fail(response, 400, 'invalid_request', 'The request could not be read.')
    }

    if (path === '/v1/owner/setup/details' && method === 'POST') {
      if (body.token !== SETUP_TOKEN) return fail(response, 400, 'setup_token_invalid', 'This setup link is invalid or has expired.')
      return send(response, 200, { totpSecret: TOTP_SECRET, totpUri: `otpauth://totp/Sesame:owner?secret=${TOTP_SECRET}&issuer=Sesame`, ownerName: '', firstOwner: true, expiresAt: minutes(60 * 20) })
    }
    if (path === '/v1/owner/setup' && method === 'POST') {
      if (body.token !== SETUP_TOKEN) return fail(response, 400, 'setup_token_invalid', 'This setup link is invalid or has expired.')
      if (body.code !== GOOD_CODE) return fail(response, 400, 'invalid_setup', 'The code is incorrect or was already used. Wait for the next code and try again.')
      state.setupRequired = false
      if (body.updateChecks === true) state.updates.enabled = true
      return send(response, 201, sessionView(startSession(response)))
    }
    if (path === '/v1/owner/login' && method === 'POST') {
      if (body.code !== GOOD_CODE || body.password !== 'correct horse battery staple') return fail(response, 401, 'invalid_credentials', 'The name, password or code is incorrect.')
      return send(response, 200, sessionView(startSession(response)))
    }

    const session = state.sessions.get(cookieOf(request) ?? '')
    if (!session) return fail(response, 401, 'not_authenticated', 'Sign in to continue.')
    if (['POST', 'PATCH', 'PUT', 'DELETE'].includes(method) && request.headers['x-sesame-csrf'] !== session.csrf) {
      return fail(response, 403, 'invalid_csrf', 'Reload the console and try again.')
    }
    const needsStepUp = () => {
      if (Date.now() < state.recentUntil) return false
      fail(response, 403, 'step_up_required', 'Confirm your password and a current code to continue.')
      return true
    }

    if (path === '/v1/owner/session') return send(response, 200, sessionView(session))
    if (path === '/v1/owner/logout') {
      state.sessions.delete(cookieOf(request))
      response.writeHead(204, { 'Set-Cookie': 'sesame_owner=; Path=/; Max-Age=0; HttpOnly' })
      return response.end()
    }
    if (path === '/v1/owner/step-up') {
      if (body.code !== GOOD_CODE || body.password !== 'correct horse battery staple') return fail(response, 401, 'invalid_credentials', 'The password or code is incorrect.')
      state.recentUntil = Date.now() + 10 * 60_000
      return send(response, 200, sessionView(session))
    }
    if (path === '/v1/owner/owners' && method === 'GET') return send(response, 200, { owners: state.owners })
    if (path === '/v1/owner/owners' && method === 'POST') {
      if (needsStepUp()) return
      const token = randomBytes(24).toString('base64url')
      const owner = { id: `own-${state.owners.length + 1}`, name: body.name, createdAt: minutes(0), lastLoginAt: null, setupPending: true, current: false }
      state.owners.push(owner)
      return send(response, 201, { owner, setupToken: token, link: `${origin}/setup#token=${token}`, expiresAt: minutes(60 * 24) })
    }
    const ownerMatch = /^\/v1\/owner\/owners\/([^/]+)$/.exec(path)
    if (ownerMatch && method === 'DELETE') {
      if (needsStepUp()) return
      state.owners = state.owners.filter((owner) => owner.id !== ownerMatch[1])
      response.writeHead(204)
      return response.end()
    }
    if (path === '/v1/owner/members' && method === 'GET') return send(response, 200, { members: state.members })
    if (path === '/v1/owner/members' && method === 'POST') {
      const member = { id: `mem-${state.members.length + 1}`, name: body.name, createdAt: minutes(0), deviceCount: 0 }
      state.members.push(member)
      return send(response, 201, { member })
    }
    const memberMatch = /^\/v1\/owner\/members\/([^/]+)$/.exec(path)
    if (memberMatch && method === 'PATCH') {
      const member = state.members.find((candidate) => candidate.id === memberMatch[1])
      if (!member) return fail(response, 404, 'not_found', 'That record does not exist.')
      member.name = body.name
      return send(response, 200, { member })
    }
    if (memberMatch && method === 'DELETE') {
      if (needsStepUp()) return
      const member = state.members.find((candidate) => candidate.id === memberMatch[1])
      state.members = state.members.filter((candidate) => candidate.id !== memberMatch[1])
      state.devices = state.devices.filter((device) => device.holder.id !== memberMatch[1])
      return send(response, 200, { revokedDevices: member?.deviceCount ?? 0 })
    }
    if (path === '/v1/owner/pairings' && method === 'GET') return send(response, 200, { pairings: state.pairings })
    if (path === '/v1/owner/pairings' && method === 'POST') {
      const member = body.memberId ? state.members.find((candidate) => candidate.id === body.memberId) : null
      if (body.memberId && !member) return fail(response, 404, 'not_found', 'That record does not exist.')
      if (member && needsStepUp()) return
      const code = randomBytes(32).toString('base64url')
      const holder = member ? { kind: 'member', id: member.id, name: member.name } : { kind: 'owner', id: 'own-1', name: 'Alex Rivera' }
      const pairing = { id: `pair-${state.pairings.length + 1}`, holder, deviceName: body.deviceName ?? '', createdBy: 'owner:own-1', createdAt: minutes(0), expiresAt: minutes(10) }
      state.pairings.push(pairing)
      return send(response, 201, { pairingId: pairing.id, code, link: `${origin}/pair#code=${code}&fp=${FINGERPRINT}`, holder, expiresAt: pairing.expiresAt })
    }
    const pairingMatch = /^\/v1\/owner\/pairings\/([^/]+)$/.exec(path)
    if (pairingMatch && method === 'DELETE') {
      state.pairings = state.pairings.filter((pairing) => pairing.id !== pairingMatch[1])
      response.writeHead(204)
      return response.end()
    }
    if (path === '/v1/owner/devices' && method === 'GET') return send(response, 200, { devices: state.devices })
    const deviceMatch = /^\/v1\/owner\/devices\/([^/]+)$/.exec(path)
    if (deviceMatch && method === 'DELETE') {
      if (needsStepUp()) return
      state.devices = state.devices.filter((device) => device.id !== deviceMatch[1])
      response.writeHead(204)
      return response.end()
    }
    if (path === '/v1/owner/audit' && method === 'GET') {
      const cursor = Number(url.searchParams.get('cursor') ?? 0)
      const rows = cursor ? state.audit.filter((entry) => entry.seq < cursor) : state.audit
      const entries = rows.slice(0, 25)
      const next = rows.length > entries.length ? entries[entries.length - 1].seq : 0
      const chain = state.chainBroken
        ? { ok: false, rows: state.audit.length, headSeq: state.audit.length, headHash: '', firstBreak: { seq: 41, reason: 'the stored hash does not match the recomputed hash' } }
        : { ok: true, rows: state.audit.length, headSeq: state.audit.length, headHash: '', firstBreak: null }
      return send(response, 200, { entries, nextCursor: next, chain })
    }
    if (path === '/v1/owner/settings' && method === 'GET') return send(response, 200, { ...state.instance, publicUrl: origin })
    if (path === '/v1/owner/settings' && method === 'PATCH') {
      if (needsStepUp()) return
      if (body.name !== undefined) state.instance.name = body.name
      return send(response, 200, { ...state.instance, publicUrl: origin })
    }
    if (path === '/v1/owner/system' && method === 'GET') {
      return send(response, 200, { version: '0.1.0', commit: '3f9a1c2d8e4b', schemaVersion: 3, databaseBytes: 1_482_752, lastBackupAt: state.lastBackupAt, warnings: state.warnings, auditChainOk: !state.chainBroken, activeOwners: 2, members: state.members.length, activeDevices: state.devices.length, pendingPairings: state.pairings.length, metricsEnabled: false, updateAvailable: state.updates.available })
    }
    if (path === '/v1/owner/updates' && method === 'GET') return send(response, 200, updateView(state.updates))
    if (path === '/v1/owner/updates' && method === 'PATCH') {
      if (needsStepUp()) return
      state.updates.enabled = body.enabled
      if (body.enabled === false) Object.assign(state.updates, { latest: null, available: false, checked: false, error: '' })
      else Object.assign(state.updates, { latest: state.updates.latest ?? '0.1.0', checked: true })
      return send(response, 200, updateView(state.updates))
    }
    if (path === '/v1/owner/updates/check' && method === 'POST') {
      if (state.updates.configured === false) return send(response, 200, updateView(state.updates))
      if (state.updates.enabled !== true) return fail(response, 409, 'updates_off', 'Update checks are off.')
      state.updates.checked = true
      return send(response, 200, updateView(state.updates))
    }
    if (path === '/v1/owner/export' && method === 'POST') {
      if (needsStepUp()) return
      return send(response, 200, { format: 'sesame-selfhost-export-v1', exportedAt: minutes(0), instance: state.instance, members: state.members, devices: state.devices, audit: { chain: { ok: true }, entries: state.audit, truncated: false } })
    }
    return fail(response, 404, 'not_found', 'The requested API endpoint does not exist.')
  }

  async function serveStatic(response, path) {
    let name = normalize(decodeURIComponent(path)).replace(/^([/\\])+/, '')
    if (name === '' || (!extname(name) && !(await exists(join(dist, name))))) name = 'index.html'
    try {
      const data = await readFile(join(dist, name))
      response.writeHead(200, { 'Content-Type': TYPES[extname(name)] ?? 'application/octet-stream', 'Content-Security-Policy': CONSOLE_CSP, 'Cache-Control': 'no-store' })
      response.end(data)
    } catch {
      response.writeHead(404)
      response.end()
    }
  }

  const server = createServer((request, response) => {
    handler(request, response).catch(() => {
      if (!response.headersSent) response.writeHead(500)
      response.end()
    })
  })
  await new Promise((resolve) => server.listen(port, '127.0.0.1', resolve))
  origin = `http://127.0.0.1:${server.address().port}`
  return {
    origin,
    state,
    setupUrl: `${origin}/setup#token=${SETUP_TOKEN}`,
    credentials: { name: 'Alex Rivera', password: 'correct horse battery staple', code: GOOD_CODE },
    close: () => new Promise((resolve) => { server.closeAllConnections(); server.close(resolve) }),
  }
}

async function exists(path) {
  try {
    await stat(path)
    return true
  } catch {
    return false
  }
}
