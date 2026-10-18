import assert from 'node:assert/strict'
import { createHmac } from 'node:crypto'
import http from 'node:http'
import https from 'node:https'

export function sleep(milliseconds) {
  return new Promise((resolveSleep) => setTimeout(resolveSleep, milliseconds))
}

export function connect(origin, { insecure = false, address = '127.0.0.1' } = {}) {
  const url = new URL(origin)
  assert.equal(url.pathname, '/', 'the origin must have no path')
  return { origin: url.origin, host: url.host, hostname: url.hostname, port: url.port, secure: url.protocol === 'https:', insecure, address }
}

export function request(instance, method, path, { headers = {}, json, host = instance.host, timeout = 10000 } = {}) {
  return new Promise((resolveRequest, reject) => {
    const body = json === undefined ? undefined : JSON.stringify(json)
    const outgoing = { host, ...headers }
    if (body !== undefined) {
      outgoing['content-type'] ??= 'application/json'
      outgoing['content-length'] = Buffer.byteLength(body)
    }
    const transport = instance.secure ? https : http
    const port = instance.port || (instance.secure ? 443 : 80)
    const options = { host: instance.address, port, method, path, headers: outgoing, agent: false }
    if (instance.secure) Object.assign(options, { servername: instance.hostname, rejectUnauthorized: !instance.insecure })
    const req = transport.request(options, (response) => {
      const chunks = []
      response.on('data', (chunk) => chunks.push(chunk))
      response.on('error', reject)
      response.on('end', () => {
        try {
          const text = Buffer.concat(chunks).toString('utf8')
          const parsed = text && /json/.test(response.headers['content-type'] ?? '') ? JSON.parse(text) : null
          resolveRequest({ status: response.statusCode, headers: response.headers, text, json: parsed })
        } catch (error) {
          reject(error)
        }
      })
    })
    req.setTimeout(timeout, () => req.destroy(new Error(`${method} ${path} timed out`)))
    req.on('error', reject)
    if (body !== undefined) req.write(body)
    req.end()
  })
}

export function expectStatus(response, status, label) {
  assert.equal(response.status, status, `${label} returned ${response.status} ${response.text.slice(0, 300)}`)
  return response
}

export function expectError(response, status, code, label) {
  expectStatus(response, status, label)
  assert.equal(response.json?.error?.code, code, `${label} error code was ${response.json?.error?.code}`)
}

function base32Decode(value) {
  const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567'
  let bits = 0
  let accumulator = 0
  const bytes = []
  for (const character of value.toUpperCase().replace(/=+$/, '')) {
    const index = alphabet.indexOf(character)
    assert.ok(index >= 0, 'the TOTP secret is not base32')
    accumulator = (accumulator << 5) | index
    bits += 5
    if (bits >= 8) {
      bytes.push((accumulator >>> (bits - 8)) & 0xff)
      bits -= 8
    }
  }
  return Buffer.from(bytes)
}

function totpFor(secret, counter) {
  const message = Buffer.alloc(8)
  message.writeBigUInt64BE(BigInt(counter))
  const digest = createHmac('sha1', base32Decode(secret)).update(message).digest()
  const offset = digest[digest.length - 1] & 0x0f
  const value = ((digest[offset] & 0x7f) << 24) | (digest[offset + 1] << 16) | (digest[offset + 2] << 8) | digest[offset + 3]
  return String(value % 1_000_000).padStart(6, '0')
}

export function authenticator(secret) {
  let lastCounter = 0
  return {
    async next() {
      for (;;) {
        const seconds = Date.now() / 1000
        const counter = Math.floor(seconds / 30)
        if (counter > lastCounter && seconds % 30 < 27) {
          lastCounter = counter
          return totpFor(secret, counter)
        }
        await sleep(500)
      }
    },
  }
}

export function ownerBrowser(instance) {
  const state = { cookie: '', csrf: '', setCookie: '' }
  const call = (method, path, { json, headers = {}, host } = {}) => {
    const outgoing = { origin: instance.origin, ...headers }
    if (state.cookie) outgoing.cookie = state.cookie
    if (state.csrf && method !== 'GET') outgoing['x-sesame-csrf'] = state.csrf
    return request(instance, method, path, { json, headers: outgoing, host })
  }
  const adopt = (response) => {
    const set = [].concat(response.headers['set-cookie'] ?? [])[0]
    assert.ok(set, 'the response set no session cookie')
    state.cookie = set.split(';')[0]
    state.setCookie = set
    state.csrf = response.json.csrfToken
    assert.ok(state.csrf, 'the session response carried no CSRF token')
  }
  return { state, call, adopt }
}

export function setupTokenFrom(text, origin) {
  const escaped = origin.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  const match = text.match(new RegExp(`${escaped}/setup#token=([A-Za-z0-9_-]{20,})`))
  assert.ok(match, `no setup link for ${origin} in the output:\n${text.slice(-1500)}`)
  return match[1]
}

export async function runFirstSetup(instance, token, { name, password }) {
  const session = ownerBrowser(instance)
  const details = expectStatus(await session.call('POST', '/v1/owner/setup/details', { json: { token } }), 200, 'setup details')
  const secret = details.json.totpSecret
  const codes = authenticator(secret)
  const response = expectStatus(await session.call('POST', '/v1/owner/setup', { json: { token, name, password, code: await codes.next() } }), 201, 'setup')
  session.adopt(response)
  return { session, secret, codes }
}

export async function ownerLogin(instance, { name, password, codes }) {
  const session = ownerBrowser(instance)
  const response = expectStatus(await session.call('POST', '/v1/owner/login', { json: { name, password, code: await codes.next() } }), 200, 'owner login')
  session.adopt(response)
  return session
}

export async function createMember(session, name) {
  const response = expectStatus(await session.call('POST', '/v1/owner/members', { json: { name } }), 201, 'create member')
  return response.json.member
}

export async function listMembers(session) {
  return expectStatus(await session.call('GET', '/v1/owner/members'), 200, 'list members').json.members
}

export async function pairDevice(instance, session, { memberId, deviceName }) {
  const pairing = expectStatus(await session.call('POST', '/v1/owner/pairings', { json: { memberId, deviceName } }), 201, 'create pairing')
  assert.ok(pairing.json.link.startsWith(`${instance.origin}/pair#code=`), `the pairing link does not start with the public address: ${pairing.json.link}`)
  const linked = expectStatus(await request(instance, 'POST', '/v1/desktop/link', { json: { code: pairing.json.code, deviceName } }), 201, 'desktop link')
  return { token: linked.json.accessToken, deviceId: linked.json.device.deviceId }
}

export async function desktopStatus(instance, token) {
  return request(instance, 'GET', '/v1/desktop/status', { headers: { authorization: `Sesame ${token}` } })
}

export async function ownerSystem(session) {
  return expectStatus(await session.call('GET', '/v1/owner/system'), 200, 'owner system').json
}
