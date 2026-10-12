import assert from 'node:assert/strict'
import { createHash, createHmac, createPublicKey, verify } from 'node:crypto'
import { spawn, spawnSync } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs'
import http from 'node:http'
import https from 'node:https'
import net from 'node:net'
import { tmpdir } from 'node:os'
import { dirname, join, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = dirname(dirname(fileURLToPath(import.meta.url)))
const consoleRoot = join(root, 'web', 'console')
const embedRoot = join(root, 'internal', 'selfhost', 'console', 'dist')
const smokeVersion = '0.0.0-smoke'
const smokeCommit = '0123456789abcdef0123456789abcdef01234567'
const ownerName = 'smoke-owner'
const ownerPassword = 'correct horse battery staple smoke'
const memberName = 'Ada Smoke'
const consoleCsp = "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; font-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

const options = parseArguments(process.argv.slice(2))
const workDir = mkdtempSync(join(options.tempRoot ?? tmpdir(), 'sesame-selfhost-smoke-'))
const servers = []
const closers = []
let passed = 0
let feedFileCounter = 0

try {
  await main()
  console.log(`Self-host smoke passed: ${passed} checks.`)
} catch (error) {
  console.error(`Self-host smoke failed: ${error.stack ?? error}`)
  for (const server of servers) console.error(`\n--- server log (port ${server.port}) ---\n${server.log().slice(-4000)}`)
  process.exitCode = 1
} finally {
  for (const server of servers) await server.stop().catch(() => {})
  for (const close of closers) await close().catch(() => {})
  if (options.keep) console.log(`Kept ${workDir}`)
  else rmSync(workDir, { recursive: true, force: true })
}

function parseArguments(args) {
  const parsed = { skipConsole: false, keep: false, binary: null, tempRoot: null }
  for (let index = 0; index < args.length; index += 1) {
    const arg = args[index]
    if (arg === '--skip-console-build') parsed.skipConsole = true
    else if (arg === '--keep') parsed.keep = true
    else if (arg === '--binary') parsed.binary = resolve(args[++index] ?? '')
    else if (arg === '--temp-root') parsed.tempRoot = resolve(args[++index] ?? '')
    else throw new Error(`Unknown argument ${arg}. Use --skip-console-build, --keep, --binary <path> or --temp-root <dir>.`)
  }
  return parsed
}

async function check(name, run) {
  try {
    const result = await run()
    passed += 1
    console.log(`ok - ${name}`)
    return result
  } catch (error) {
    error.message = `${name}: ${error.message}`
    throw error
  }
}

function run(command, args, spawnOptions = {}) {
  const result = spawnSync(command, args, { encoding: 'utf8', ...spawnOptions })
  if (result.error) throw result.error
  return result
}

function mustRun(command, args, spawnOptions = {}) {
  const result = run(command, args, { stdio: ['ignore', 'pipe', 'pipe'], ...spawnOptions })
  if (result.status !== 0) throw new Error(`${command} ${args.join(' ')} exited ${result.status}\n${result.stdout}${result.stderr}`)
  return result
}

function buildConsole() {
  if (options.skipConsole) {
    assert.ok(existsSync(join(consoleRoot, 'dist', 'index.html')), 'web/console/dist is missing, so --skip-console-build cannot be used')
    return
  }
  if (!existsSync(join(consoleRoot, 'node_modules'))) mustRun('npm', ['ci'], { cwd: consoleRoot })
  mustRun('npm', ['run', 'build'], { cwd: consoleRoot })
}

function embedOverlay() {
  const dist = join(consoleRoot, 'dist')
  const replace = {}
  const walk = (directory) => {
    for (const entry of readdirSync(directory, { withFileTypes: true })) {
      const path = join(directory, entry.name)
      if (entry.isDirectory()) walk(path)
      else replace[join(embedRoot, relative(dist, path))] = path
    }
  }
  walk(dist)
  const file = join(workDir, 'embed-overlay.json')
  writeFileSync(file, JSON.stringify({ Replace: replace }))
  return file
}

function buildBinary() {
  if (options.binary) return options.binary
  const output = join(workDir, 'sesame-server')
  const ldflags = `-s -w -X usesesame.app/backend/internal/buildinfo.Version=${smokeVersion} -X usesesame.app/backend/internal/buildinfo.Commit=${smokeCommit}`
  mustRun('go', ['build', '-trimpath', '-overlay', embedOverlay(), `-ldflags=${ldflags}`, '-o', output, './cmd/sesame-server'], {
    cwd: root,
    env: { ...process.env, CGO_ENABLED: '0' },
  })
  return output
}

function freePort() {
  return new Promise((resolvePort, reject) => {
    const probe = net.createServer()
    probe.once('error', reject)
    probe.listen(0, '127.0.0.1', () => {
      const { port } = probe.address()
      probe.close(() => resolvePort(port))
    })
  })
}

function serverEnvironment(dataDir, port, extra = {}) {
  const environment = Object.fromEntries(Object.entries(process.env).filter(([name]) => !name.startsWith('SESAME_')))
  return {
    ...environment,
    SESAME_DATA_DIR: dataDir,
    SESAME_ADDR: `127.0.0.1:${port}`,
    SESAME_PUBLIC_URL: `http://localhost:${port}`,
    SESAME_BACKUP_INTERVAL: '0',
    SESAME_LOG_LEVEL: 'info',
    ...extra,
  }
}

async function startServer(binary, dataDir, extra = {}) {
  const port = await freePort()
  const child = spawn(binary, ['serve'], { env: serverEnvironment(dataDir, port, extra), stdio: ['ignore', 'pipe', 'pipe'] })
  let output = ''
  child.stdout.on('data', (chunk) => { output += chunk })
  child.stderr.on('data', (chunk) => { output += chunk })
  const exited = new Promise((resolveExit) => child.once('exit', (code, signal) => resolveExit({ code, signal })))
  const server = {
    port,
    origin: `http://localhost:${port}`,
    log: () => output,
    exited,
    async stop() {
      if (child.exitCode !== null || child.signalCode !== null) return exited
      child.kill('SIGTERM')
      return exited
    },
  }
  servers.push(server)
  const deadline = Date.now() + 20000
  while (Date.now() < deadline) {
    if (child.exitCode !== null) throw new Error(`the server exited early with code ${child.exitCode}\n${output}`)
    if (output.includes('Sesame server listening')) return server
    await sleep(50)
  }
  throw new Error('the server did not report that it was listening within 20 seconds')
}

function buildFeedsign() {
  const output = join(workDir, 'feedsign')
  mustRun('go', ['build', '-trimpath', '-o', output, './cmd/feedsign'], { cwd: root, env: { ...process.env, CGO_ENABLED: '0' } })
  return output
}

function makeSigningKey(tool, name, fileName = name) {
  const keyFile = join(workDir, 'keys', `${fileName}.key`)
  mkdirSync(dirname(keyFile), { recursive: true })
  const result = mustRun(tool, ['keygen', '-id', name, '-out', keyFile])
  return { name, keyFile, entry: result.stdout.trim().split('\n').at(-1), stdout: result.stdout }
}

function signFeed(tool, key, sequence, version) {
  const platform = { x64: 'amd64', arm64: 'arm64' }[process.arch] ?? process.arch
  const stamp = (milliseconds) => new Date(milliseconds).toISOString().replace(/\.\d{3}Z$/, 'Z')
  const payload = {
    schemaVersion: 1,
    issuedAt: stamp(Date.now() - 3600_000),
    expiresAt: stamp(Date.now() + 30 * 86_400_000),
    products: [{
      id: 'sesame-server',
      channels: {
        stable: {
          version,
          publishedAt: stamp(Date.now() - 86_400_000),
          notesUrl: `https://example.net/notes/${version}`,
          security: true,
          minimumFrom: '',
          images: [{ ref: `registry.example.net/sesame/server@sha256:${'a'.repeat(64)}` }],
          binaries: [{ os: process.platform, arch: platform, url: `https://downloads.example.net/sesame-server-${process.platform}-${platform}`, sha256: 'b'.repeat(64) }],
        },
      },
    }],
  }
  feedFileCounter += 1
  const directory = join(workDir, 'feeds')
  mkdirSync(directory, { recursive: true })
  const input = join(directory, `payload-${feedFileCounter}.json`)
  const output = join(directory, `signed-${feedFileCounter}.json`)
  writeFileSync(input, JSON.stringify(payload))
  mustRun(tool, ['sign', '-key', key.keyFile, '-sequence', String(sequence), '-in', input, '-out', output])
  return readFileSync(output)
}

function makeCertificate() {
  const directory = join(workDir, 'tls')
  mkdirSync(directory, { recursive: true })
  const keyFile = join(directory, 'key.pem')
  const certFile = join(directory, 'cert.pem')
  mustRun('openssl', ['req', '-x509', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:prime256v1', '-nodes', '-keyout', keyFile, '-out', certFile, '-days', '2', '-subj', '/CN=127.0.0.1', '-addext', 'subjectAltName=IP:127.0.0.1'])
  return { keyFile, certFile }
}

async function startFakeFeed(certificate) {
  const port = await freePort()
  const feed = { port, url: `https://127.0.0.1:${port}/feed.json`, body: Buffer.from('{}'), requests: [] }
  const server = https.createServer({ key: readFileSync(certificate.keyFile), cert: readFileSync(certificate.certFile) }, (incoming, outgoing) => {
    feed.requests.push({ method: incoming.method, url: incoming.url, headers: incoming.headers })
    outgoing.setHeader('content-type', 'application/json')
    outgoing.end(feed.body)
  })
  await new Promise((resolveListen, reject) => {
    server.once('error', reject)
    server.listen(port, '127.0.0.1', resolveListen)
  })
  closers.push(() => new Promise((resolveClose) => { server.close(resolveClose); server.closeAllConnections() }))
  return feed
}

function sleep(milliseconds) {
  return new Promise((resolveSleep) => setTimeout(resolveSleep, milliseconds))
}

function request(server, method, path, { headers = {}, json, host = `localhost:${server.port}` } = {}) {
  return new Promise((resolveRequest, reject) => {
    const body = json === undefined ? undefined : JSON.stringify(json)
    const outgoing = { host, ...headers }
    if (body !== undefined) {
      outgoing['content-type'] ??= 'application/json'
      outgoing['content-length'] = Buffer.byteLength(body)
    }
    const req = http.request({ host: '127.0.0.1', port: server.port, method, path, headers: outgoing, agent: false }, (response) => {
      const chunks = []
      response.on('data', (chunk) => chunks.push(chunk))
      response.on('end', () => {
        const raw = Buffer.concat(chunks)
        const text = raw.toString('utf8')
        let parsed = null
        if (text && /json/.test(response.headers['content-type'] ?? '')) parsed = JSON.parse(text)
        resolveRequest({ status: response.statusCode, headers: response.headers, raw, text, json: parsed })
      })
    })
    req.setTimeout(10000, () => req.destroy(new Error(`${method} ${path} timed out`)))
    req.on('error', reject)
    if (body !== undefined) req.write(body)
    req.end()
  })
}

function expectStatus(response, status, label) {
  assert.equal(response.status, status, `${label} returned ${response.status} ${response.text.slice(0, 300)}`)
  return response
}

function expectError(response, status, code, label) {
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

function totp(secret, offsetSeconds = 0) {
  const counter = BigInt(Math.floor((Date.now() / 1000 + offsetSeconds) / 30))
  const message = Buffer.alloc(8)
  message.writeBigUInt64BE(counter)
  const digest = createHmac('sha1', base32Decode(secret)).update(message).digest()
  const offset = digest[digest.length - 1] & 0x0f
  const value = ((digest[offset] & 0x7f) << 24) | (digest[offset + 1] << 16) | (digest[offset + 2] << 8) | digest[offset + 3]
  return String(value % 1_000_000).padStart(6, '0')
}

async function clearOfPeriodEdge() {
  const intoPeriod = (Date.now() / 1000) % 30
  if (intoPeriod > 27) await sleep((30 - intoPeriod + 1) * 1000)
}

function browser(server) {
  const state = { cookie: '', csrf: '' }
  const call = (method, path, { json, headers = {}, host } = {}) => {
    const outgoing = { origin: server.origin, ...headers }
    if (state.cookie) outgoing.cookie = state.cookie
    if (state.csrf && method !== 'GET') outgoing['x-sesame-csrf'] = state.csrf
    return request(server, method, path, { json, headers: outgoing, host })
  }
  const adopt = (response) => {
    const set = [].concat(response.headers['set-cookie'] ?? [])[0]
    assert.ok(set, 'the response set no session cookie')
    state.cookie = set.split(';')[0]
    state.csrf = response.json.csrfToken
    assert.ok(state.csrf, 'the session response carried no CSRF token')
    return set
  }
  return { state, call, adopt }
}

function cli(binary, dataDir, port, args, extra = {}) {
  return run(binary, args, { env: serverEnvironment(dataDir, port, extra), stdio: ['ignore', 'pipe', 'pipe'] })
}

async function main() {
  await check('console builds', async () => buildConsole())
  const binary = await check('server builds statically with the console embedded', async () => buildBinary())

  const feedTool = await check('feedsign builds', async () => buildFeedsign())
  const signingKey = await check('feedsign makes a throwaway key and keeps the private half out of its output', async () => {
    const key = makeSigningKey(feedTool, 'smoke-key')
    assert.match(key.entry, /^smoke-key:[A-Za-z0-9_-]{43}$/)
    if (process.platform !== 'win32') assert.equal(statSync(key.keyFile).mode & 0o777, 0o600, 'the private key file is not private')
    const seed = readFileSync(key.keyFile, 'utf8').trim().split(':')[1]
    assert.ok(seed && !key.stdout.includes(seed), 'feedsign printed the private key')
    return key
  })
  const certificate = await check('a local TLS certificate is made for the fake feed', async () => makeCertificate())
  const feed = await startFakeFeed(certificate)
  feed.body = signFeed(feedTool, signingKey, 5, '99.0.0')
  const updateEnvironment = {
    SESAME_UPDATE_FEED_URL: feed.url,
    SESAME_UPDATE_PUBLIC_KEYS: signingKey.entry,
    SESAME_INSTALL_KIND: 'binary',
    SSL_CERT_FILE: certificate.certFile,
  }

  const dataDir = join(workDir, 'data')
  const restoredDir = join(workDir, 'restored')
  const first = await startServer(binary, dataDir, updateEnvironment)
  const setupToken = await check('first start logs a setup link', async () => {
    const match = first.log().match(new RegExp(`${first.origin.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}/setup#token=([A-Za-z0-9_-]{20,})`))
    assert.ok(match, 'no setup link in the log')
    return match[1]
  })

  await check('data directory is private to its owner', async () => {
    if (process.platform === 'win32') return
    assert.equal(statSync(dataDir).mode & 0o077, 0, 'data directory is open to other users')
    for (const file of ['signing.key', 'admin-encryption.key', 'ip-pepper.key']) {
      assert.equal(statSync(join(dataDir, 'secrets', file)).mode & 0o077, 0, `${file} is open to other users`)
    }
  })

  await check('GET /livez reports the build', async () => {
    const response = expectStatus(await request(first, 'GET', '/livez'), 200, '/livez')
    assert.equal(response.json.status, 'ok')
    assert.equal(response.json.service, 'sesame-server')
    if (!options.binary) {
      assert.equal(response.json.version, smokeVersion)
      assert.equal(response.json.commit, smokeCommit)
    }
  })

  await check('GET /readyz reports the database ready', async () => {
    const response = expectStatus(await request(first, 'GET', '/readyz'), 200, '/readyz')
    assert.equal(response.json.database, 'ready')
  })

  const instance = await check('GET /v1/instance describes a self-host profile', async () => {
    const response = expectStatus(await request(first, 'GET', '/v1/instance'), 200, '/v1/instance')
    assert.equal(response.json.profile, 'selfhost')
    assert.equal(response.json.apiVersion, 1)
    assert.equal(response.json.setupRequired, true)
    assert.deepEqual(response.json.modules, [])
    const raw = Buffer.from(response.json.capabilityPublicKey, 'base64url')
    assert.equal(raw.length, 32)
    assert.equal(response.json.fingerprint, createHash('sha256').update(raw).digest('hex'))
    return response.json
  })

  await check('GET /v1/capabilities carries a signature the instance key verifies', async () => {
    const response = expectStatus(await request(first, 'GET', '/v1/capabilities'), 200, '/v1/capabilities')
    const key = createPublicKey({ key: { kty: 'OKP', crv: 'Ed25519', x: instance.capabilityPublicKey }, format: 'jwk' })
    const payload = Buffer.from(response.json.payload, 'base64url')
    assert.equal(verify(null, payload, key, Buffer.from(response.json.signature, 'base64url')), true, 'signature does not verify')
    const document = JSON.parse(payload.toString('utf8'))
    assert.equal(document.features.desktopLinking, true)
    assert.equal(document.features.sync, false)
    assert.equal(response.json.keyId, instance.capabilityKeyId)
  })

  await check('GET /config.json asks for setup', async () => {
    const response = expectStatus(await request(first, 'GET', '/config.json'), 200, '/config.json')
    assert.equal(response.json.setupRequired, true)
    assert.equal(response.json.apiBase, '')
  })

  await check('the console index carries the strict CSP and serves its assets', async () => {
    const index = expectStatus(await request(first, 'GET', '/'), 200, '/')
    assert.match(index.headers['content-type'], /text\/html/)
    assert.equal(index.headers['content-security-policy'], consoleCsp)
    assert.match(index.headers['cache-control'], /no-cache/)
    const script = index.text.match(/src="\.\/(assets\/[^"]+\.js)"/)?.[1]
    assert.ok(script, 'index.html references no script')
    const asset = expectStatus(await request(first, 'GET', `/${script}`), 200, `/${script}`)
    assert.match(asset.headers['cache-control'], /max-age=31536000/)
    assert.equal(asset.headers['content-security-policy'], consoleCsp)
    const route = expectStatus(await request(first, 'GET', '/devices'), 200, '/devices')
    assert.equal(route.text, index.text)
    expectError(await request(first, 'GET', '/assets/missing.js'), 404, 'not_found', '/assets/missing.js')
    expectError(await request(first, 'GET', '/v1/unknown'), 404, 'not_found', '/v1/unknown')
  })

  const owner = browser(first)
  const ownerSecret = await check('setup details return the TOTP secret and reject a bad token', async () => {
    expectError(await owner.call('POST', '/v1/owner/setup/details', { json: { token: 'not-a-real-token-not-a-real-token' } }), 400, 'setup_token_invalid', 'setup details with a bad token')
    expectStatus(await request(first, 'POST', '/v1/owner/setup/details', { json: { token: setupToken } }), 403, 'setup details without an Origin header')
    const response = expectStatus(await owner.call('POST', '/v1/owner/setup/details', { json: { token: setupToken } }), 200, 'setup details')
    assert.equal(response.json.firstOwner, true)
    assert.match(response.json.totpUri, /^otpauth:\/\/totp\//)
    return response.json.totpSecret
  })

  await clearOfPeriodEdge()
  await check('first-run setup creates the owner and signs them in', async () => {
    const wrong = await owner.call('POST', '/v1/owner/setup', { json: { token: setupToken, name: ownerName, password: ownerPassword, code: '000000' } })
    expectError(wrong, 400, 'invalid_setup', 'setup with a wrong code')
    const response = expectStatus(await owner.call('POST', '/v1/owner/setup', { json: { token: setupToken, name: ownerName, password: ownerPassword, code: totp(ownerSecret, -30) } }), 201, 'setup')
    const cookie = owner.adopt(response)
    assert.match(cookie, /^sesame_owner=/)
    assert.match(cookie, /HttpOnly/i)
    assert.match(cookie, /SameSite=Strict/i)
    assert.doesNotMatch(cookie, /Secure/i)
    assert.equal(response.json.owner.name, ownerName)
  })

  await check('the setup token works once and setup is no longer required', async () => {
    const again = browser(first)
    expectError(await again.call('POST', '/v1/owner/setup', { json: { token: setupToken, name: 'second-owner', password: ownerPassword, code: totp(ownerSecret, 30) } }), 400, 'setup_token_invalid', 'setup token reuse')
    const config = expectStatus(await request(first, 'GET', '/config.json'), 200, '/config.json')
    assert.equal(config.json.setupRequired, false)
  })

  await check('owner login rejects a replayed code, a wrong password and a missing Origin', async () => {
    const session = browser(first)
    const code = totp(ownerSecret)
    const login = expectStatus(await session.call('POST', '/v1/owner/login', { json: { name: ownerName, password: ownerPassword, code } }), 200, 'login')
    session.adopt(login)
    expectError(await browser(first).call('POST', '/v1/owner/login', { json: { name: ownerName, password: ownerPassword, code } }), 401, 'invalid_credentials', 'login with a used code')
    expectError(await browser(first).call('POST', '/v1/owner/login', { json: { name: ownerName, password: `${ownerPassword}x`, code: totp(ownerSecret, 30) } }), 401, 'invalid_credentials', 'login with a wrong password')
    const noOrigin = await request(first, 'POST', '/v1/owner/login', { json: { name: ownerName, password: ownerPassword, code: totp(ownerSecret, 30) } })
    expectError(noOrigin, 403, 'origin_not_allowed', 'login without Origin')
    owner.state.cookie = session.state.cookie
    owner.state.csrf = session.state.csrf
  })

  await check('a wrong Host header is refused on owner and desktop routes', async () => {
    for (const host of ['evil.example', `127.0.0.1:${first.port}`, 'localhost:9']) {
      expectError(await owner.call('GET', '/v1/owner/session', { host }), 421, 'host_mismatch', `owner route with Host ${host}`)
      expectError(await request(first, 'GET', '/v1/desktop/status', { host }), 421, 'host_mismatch', `desktop route with Host ${host}`)
    }
    expectStatus(await request(first, 'GET', '/livez', { host: 'evil.example' }), 200, '/livez with any Host')
    expectStatus(await request(first, 'GET', '/readyz', { host: 'evil.example' }), 200, '/readyz with any Host')
    const spoofed = await owner.call('GET', '/v1/owner/session', { host: 'evil.example', headers: { 'x-forwarded-host': `localhost:${first.port}` } })
    expectError(spoofed, 421, 'host_mismatch', 'X-Forwarded-Host must be ignored')
    const trailingDot = await owner.call('GET', '/v1/owner/session', { host: `LOCALHOST.:${first.port}` })
    expectStatus(trailingDot, 200, 'case and trailing dot are ignored')
  })

  await check('owner mutations need the CSRF token and the console origin', async () => {
    const noCsrf = await request(first, 'POST', '/v1/owner/members', { json: { name: 'No Csrf' }, headers: { origin: first.origin, cookie: owner.state.cookie } })
    expectError(noCsrf, 403, 'invalid_csrf', 'member without CSRF')
    const foreign = await owner.call('POST', '/v1/owner/members', { json: { name: 'Foreign' }, headers: { origin: 'https://evil.example' } })
    expectError(foreign, 403, 'origin_not_allowed', 'member from another origin')
    expectError(await request(first, 'GET', '/v1/owner/members'), 401, 'not_authenticated', 'members without a session')
  })

  const member = await check('owner creates a member', async () => {
    const response = expectStatus(await owner.call('POST', '/v1/owner/members', { json: { name: memberName } }), 201, 'create member')
    assert.equal(response.json.member.name, memberName)
    const list = expectStatus(await owner.call('GET', '/v1/owner/members'), 200, 'list members')
    assert.equal(list.json.members.length, 1)
    return response.json.member
  })

  const pairing = await check('owner creates a pairing for the member', async () => {
    const response = expectStatus(await owner.call('POST', '/v1/owner/pairings', { json: { memberId: member.id, deviceName: 'Smoke laptop' } }), 201, 'create pairing')
    assert.ok(response.json.code.length >= 32 && response.json.code.length <= 128)
    assert.equal(response.json.link, `${first.origin}/pair#code=${response.json.code}&fp=${instance.fingerprint}`)
    assert.equal(response.json.holder.kind, 'member')
    const pending = expectStatus(await owner.call('GET', '/v1/owner/pairings'), 200, 'list pairings')
    assert.equal(pending.json.pairings.length, 1)
    assert.ok(!JSON.stringify(pending.json).includes(response.json.code), 'the pairing list leaks the code')
    return response.json
  })

  const desktop = await check('the desktop redeems the pairing at POST /v1/desktop/link', async () => {
    const response = expectStatus(await request(first, 'POST', '/v1/desktop/link', { json: { code: pairing.code, deviceName: 'Linux desktop' } }), 201, 'desktop link')
    assert.equal(response.json.syncAvailable, false)
    assert.ok(response.json.accessToken.length >= 32)
    assert.equal(response.json.device.deviceName, 'Linux desktop')
    expectError(await request(first, 'POST', '/v1/desktop/link', { json: { code: pairing.code, deviceName: 'Linux desktop' } }), 401, 'invalid_desktop_link', 'second use of the code')
    expectError(await request(first, 'POST', '/v1/desktop/link', { json: { code: 'short', deviceName: 'Linux desktop' } }), 400, 'invalid_desktop_link', 'code that is too short')
    expectError(await request(first, 'POST', '/v1/desktop/link', { json: { code: pairing.code, deviceName: 'Linux desktop' }, headers: { origin: first.origin } }), 403, 'origin_not_allowed', 'desktop link from a browser')
    return { token: response.json.accessToken, deviceId: response.json.device.deviceId }
  })

  const authorization = { authorization: `Sesame ${desktop.token}` }
  await check('the device token works for status, heartbeat and config', async () => {
    const status = expectStatus(await request(first, 'GET', '/v1/desktop/status', { headers: authorization }), 200, 'desktop status')
    assert.equal(status.json.connected, true)
    assert.equal(status.json.syncAvailable, false)
    assert.equal(status.json.device.deviceId, desktop.deviceId)
    const heartbeat = expectStatus(await request(first, 'POST', '/v1/desktop/heartbeat', {
      headers: authorization,
      json: { appVersion: '0.0.0', platform: 'linux', architecture: 'x86_64', updateChannel: 'beta', protocolVersion: 1, browserHelperCapable: true, browserHelperObserved: false },
    }), 200, 'desktop heartbeat')
    assert.equal(heartbeat.json.device.platform, 'linux')
    const config = expectStatus(await request(first, 'GET', '/v1/desktop/config', { headers: authorization }), 200, 'desktop config')
    assert.equal(config.json.syncAvailable, false)
    const bearer = await request(first, 'GET', '/v1/desktop/status', { headers: { authorization: `Bearer ${desktop.token}` } })
    expectStatus(bearer, 200, 'Bearer scheme')
    expectError(await request(first, 'GET', '/v1/desktop/status'), 401, 'not_authenticated', 'status without a token')
    expectError(await request(first, 'GET', '/v1/desktop/status', { headers: { authorization: `Sesame ${desktop.token}x` } }), 401, 'not_authenticated', 'status with a changed token')
  })

  await check('the owner sees the device and revokes it', async () => {
    const devices = expectStatus(await owner.call('GET', '/v1/owner/devices'), 200, 'list devices')
    const found = devices.json.devices.find((device) => device.id === desktop.deviceId)
    assert.ok(found, 'the linked device is not listed')
    assert.equal(found.holder.kind, 'member')
    assert.equal(found.holder.name, memberName)
    expectStatus(await owner.call('DELETE', `/v1/owner/devices/${desktop.deviceId}`), 204, 'revoke device')
  })

  await check('the revoked token is refused everywhere', async () => {
    expectError(await request(first, 'GET', '/v1/desktop/status', { headers: authorization }), 401, 'not_authenticated', 'status after revocation')
    expectError(await request(first, 'POST', '/v1/desktop/heartbeat', { headers: authorization, json: { appVersion: '0.0.0', platform: 'linux', architecture: 'x86_64', updateChannel: 'beta', protocolVersion: 1, browserHelperCapable: true, browserHelperObserved: false } }), 401, 'not_authenticated', 'heartbeat after revocation')
    expectStatus(await request(first, 'DELETE', '/v1/desktop/connection', { headers: authorization }), 204, 'disconnect after revocation')
    const active = expectStatus(await owner.call('GET', '/v1/owner/devices'), 200, 'list devices')
    assert.equal(active.json.devices.some((device) => device.id === desktop.deviceId), false)
    const all = expectStatus(await owner.call('GET', '/v1/owner/devices?includeInactive=true'), 200, 'list all devices')
    assert.ok(all.json.devices.find((device) => device.id === desktop.deviceId)?.revokedAt, 'the revoked device has no revokedAt')
  })

  await check('update checks start unset, the page asks and the server makes no request', async () => {
    expectError(await request(first, 'GET', '/v1/owner/updates'), 401, 'not_authenticated', 'updates without a session')
    const response = expectStatus(await owner.call('GET', '/v1/owner/updates'), 200, 'updates')
    assert.equal(response.json.configured, true)
    assert.equal(response.json.enabled, null)
    assert.equal(response.json.channel, 'stable')
    assert.equal(response.json.installKind, 'binary')
    assert.equal(response.json.current.product, 'sesame-server')
    assert.equal(response.json.latest, null)
    assert.equal(response.json.available, false)
    assert.equal(response.json.checkedAt, null)
    assert.equal(response.json.error, '')
    assert.deepEqual(response.json.commands, [])
    expectError(await owner.call('POST', '/v1/owner/updates/check'), 409, 'updates_off', 'check while unset')
    const system = expectStatus(await owner.call('GET', '/v1/owner/system'), 200, 'system')
    assert.equal(system.json.updateAvailable, false)
    assert.equal(feed.requests.length, 0, 'the server called the feed before the owner allowed it')
  })

  await check('update settings refuse a wrong CSRF token, a foreign origin and a bad body', async () => {
    const wrongCsrf = await request(first, 'PATCH', '/v1/owner/updates', { json: { enabled: true }, headers: { origin: first.origin, cookie: owner.state.cookie, 'x-sesame-csrf': 'wrong' } })
    expectError(wrongCsrf, 403, 'invalid_csrf', 'update settings with a wrong CSRF token')
    expectError(await owner.call('PATCH', '/v1/owner/updates', { json: { enabled: true }, headers: { origin: 'https://evil.example' } }), 403, 'origin_not_allowed', 'update settings from another origin')
    expectError(await owner.call('POST', '/v1/owner/updates/check', { headers: { origin: 'https://evil.example' } }), 403, 'origin_not_allowed', 'update check from another origin')
    expectError(await owner.call('PATCH', '/v1/owner/updates', { json: { channel: 'nightly' } }), 400, 'invalid_updates', 'unknown channel')
    expectError(await owner.call('PATCH', '/v1/owner/updates', { json: {} }), 400, 'invalid_updates', 'empty settings')
    assert.equal(feed.requests.length, 0)
  })

  await check('turning checks on fetches the signed feed and offers the update with commands', async () => {
    const response = expectStatus(await owner.call('PATCH', '/v1/owner/updates', { json: { enabled: true } }), 200, 'turn checks on')
    assert.equal(feed.requests.length, 1, 'turning checks on should fetch the feed once')
    assert.equal(response.json.enabled, true)
    assert.equal(response.json.error, '')
    assert.equal(response.json.available, true)
    assert.ok(response.json.checkedAt, 'no check time')
    assert.equal(response.json.latest.version, '99.0.0')
    assert.equal(response.json.latest.security, true)
    assert.equal(response.json.latest.notesUrl, 'https://example.net/notes/99.0.0')
    assert.equal(response.json.latest.images.length, 1)
    assert.equal(response.json.latest.binaries[0].sha256, 'b'.repeat(64))
    const texts = response.json.commands.map((command) => command.text)
    assert.equal(texts.length, 4, `commands: ${texts.join(' | ')}`)
    assert.match(texts[0], /^curl -fsSLo sesame-server\.new --proto '=https' --proto-redir '=https' 'https:\/\/downloads\.example\.net\/sesame-server-/)
    assert.ok(texts[1].includes('b'.repeat(64)), 'the hash comparison does not name the feed hash')
    assert.match(texts[3], /systemctl restart sesame-server/)
    assert.ok(response.json.commands.every((command) => command.label), 'a command has no label')
    const system = expectStatus(await owner.call('GET', '/v1/owner/system'), 200, 'system')
    assert.equal(system.json.updateAvailable, true)
    const again = expectStatus(await owner.call('GET', '/v1/owner/updates'), 200, 'updates again')
    assert.equal(again.json.latest.version, '99.0.0')
    assert.equal(feed.requests.length, 1, 'reading the document must not call the feed')
  })

  await check('the feed request has no query, cookie, version or instance id', async () => {
    const seen = feed.requests[0]
    const current = expectStatus(await owner.call('GET', '/v1/owner/updates'), 200, 'updates').json.current.version
    assert.equal(seen.method, 'GET')
    assert.equal(seen.url, '/feed.json')
    const allowed = new Set(['host', 'accept', 'accept-encoding', 'user-agent'])
    for (const name of Object.keys(seen.headers)) assert.ok(allowed.has(name), `unexpected request header ${name}`)
    const everything = JSON.stringify(seen)
    for (const secret of [current, instance.instanceId, instance.fingerprint, owner.state.cookie, owner.state.csrf, ownerName]) {
      assert.ok(!everything.includes(secret), `the request holds ${secret}`)
    }
  })

  await check('an on demand check works and a rolled back feed is refused with the last result kept', async () => {
    const checked = expectStatus(await owner.call('POST', '/v1/owner/updates/check'), 200, 'check')
    assert.equal(checked.json.error, '')
    assert.equal(feed.requests.length, 2)
    feed.body = signFeed(feedTool, signingKey, 4, '99.5.0')
    const rolledBack = expectStatus(await owner.call('POST', '/v1/owner/updates/check'), 200, 'check of an older feed')
    assert.equal(rolledBack.json.error, 'feed_rollback')
    assert.equal(rolledBack.json.latest.version, '99.0.0')
    assert.equal(rolledBack.json.available, true)
  })

  await check('a feed signed by another key is refused with the last result kept', async () => {
    const other = makeSigningKey(feedTool, 'smoke-key', 'attacker')
    feed.body = signFeed(feedTool, other, 50, '99.9.0')
    expectStatus(await owner.call('PATCH', '/v1/owner/updates', { json: { enabled: false } }), 200, 'turn checks off')
    const refused = expectStatus(await owner.call('PATCH', '/v1/owner/updates', { json: { enabled: true } }), 200, 'turn checks on against a forged feed')
    assert.equal(refused.json.error, 'feed_invalid')
    assert.equal(refused.json.latest.version, '99.0.0')
    assert.equal(refused.json.available, true)
  })

  await check('the check route allows three requests per hour per owner', async () => {
    const before = feed.requests.length
    const limited = await owner.call('POST', '/v1/owner/updates/check')
    expectError(limited, 429, 'too_many_attempts', 'fourth check in an hour')
    assert.ok(Number(limited.headers['retry-after']) > 0, 'no Retry-After')
    assert.equal(feed.requests.length, before, 'a limited check still called the feed')
  })

  await check('turning checks off stops requests and turning them on again recovers', async () => {
    const before = feed.requests.length
    const off = expectStatus(await owner.call('PATCH', '/v1/owner/updates', { json: { enabled: false } }), 200, 'turn checks off')
    assert.equal(off.json.enabled, false)
    assert.equal(off.json.latest, null)
    assert.equal(off.json.available, false)
    assert.deepEqual(off.json.commands, [])
    assert.equal(expectStatus(await owner.call('GET', '/v1/owner/system'), 200, 'system').json.updateAvailable, false)
    assert.equal(feed.requests.length, before)
    feed.body = signFeed(feedTool, signingKey, 6, '99.0.0')
    const on = expectStatus(await owner.call('PATCH', '/v1/owner/updates', { json: { enabled: true } }), 200, 'turn checks back on')
    assert.equal(on.json.error, '')
    assert.equal(on.json.latest.version, '99.0.0')
    assert.equal(feed.requests.length, before + 1)
  })

  await check('the audit chain is intact and records the run', async () => {
    const response = expectStatus(await owner.call('GET', '/v1/owner/audit?limit=200'), 200, 'audit')
    assert.equal(response.json.chain.ok, true)
    assert.ok(response.json.entries.length >= 5, 'too few audit entries')
    const actions = response.json.entries.map((entry) => entry.action).join(' ')
    assert.match(actions, /pair/i)
    assert.match(actions, /revok/i)
    assert.match(actions, /updates\.updated/)
    assert.match(actions, /updates\.rollback_rejected/)
    const system = expectStatus(await owner.call('GET', '/v1/owner/system'), 200, 'system')
    assert.equal(system.json.auditChainOk, true)
    assert.equal(system.json.activeDevices, 0)
  })

  await check('a health probe from the CLI succeeds while the server runs', async () => {
    const result = cli(binary, dataDir, first.port, ['healthcheck'])
    assert.equal(result.status, 0, result.stderr)
  })

  const backupFile = join(workDir, 'backup', 'smoke.tar')
  await check('backup runs while the server is up and the archive checks out', async () => {
    mkdirSync(dirname(backupFile), { recursive: true })
    const made = cli(binary, dataDir, first.port, ['backup', backupFile])
    assert.equal(made.status, 0, made.stderr)
    assert.ok(existsSync(backupFile))
    if (process.platform !== 'win32') assert.equal(statSync(backupFile).mode & 0o077, 0, 'the backup is open to other users')
    const verified = cli(binary, dataDir, first.port, ['check', backupFile])
    assert.equal(verified.status, 0, verified.stderr)
    assert.match(verified.stdout, /is intact/)
    assert.match(verified.stdout, /secrets match the database: true/)
    const again = cli(binary, dataDir, first.port, ['backup', backupFile])
    assert.notEqual(again.status, 0, 'backup overwrote an existing file')
  })

  await check('check, export and owner reset also work while the server runs', async () => {
    const live = cli(binary, dataDir, first.port, ['check'])
    assert.equal(live.status, 0, live.stderr)
    const exported = cli(binary, dataDir, first.port, ['export'])
    assert.equal(exported.status, 0, exported.stderr)
    const reset = cli(binary, dataDir, first.port, ['owner', 'reset', ownerName])
    assert.equal(reset.status, 0, reset.stderr)
    assert.match(reset.stdout, /\/setup#token=/)
    expectError(await owner.call('GET', '/v1/owner/session'), 401, 'session_expired', 'the reset ends the owner sessions')
    const fresh = browser(first)
    const code = totp(ownerSecret, 30)
    expectError(await fresh.call('POST', '/v1/owner/login', { json: { name: ownerName, password: ownerPassword, code } }), 401, 'invalid_credentials', 'login while the reset is pending')
  })

  await check('a second server and a restore both refuse a data directory in use', async () => {
    const port = await freePort()
    const result = run(binary, ['serve'], { env: serverEnvironment(dataDir, port), stdio: ['ignore', 'pipe', 'pipe'], timeout: 15000 })
    assert.notEqual(result.status, 0)
    assert.match(result.stderr, /already using/)
    const restore = cli(binary, dataDir, port, ['restore', backupFile])
    assert.notEqual(restore.status, 0, 'restore ran over a live data directory')
    assert.match(restore.stderr, /stop the running server/)
  })

  await check('the server stops cleanly on SIGTERM', async () => {
    const outcome = await first.stop()
    assert.equal(outcome.code, 0, `exit ${outcome.code} ${outcome.signal}\n${first.log().slice(-1000)}`)
  })

  await check('the live database passes its checks and exports without secrets', async () => {
    const live = cli(binary, dataDir, first.port, ['check'])
    assert.equal(live.status, 0, live.stderr)
    assert.match(live.stdout, /passed its checks/)
    const exported = cli(binary, dataDir, first.port, ['export'])
    assert.equal(exported.status, 0, exported.stderr)
    const document = JSON.parse(exported.stdout)
    assert.equal(document.format, 'sesame-selfhost-export-v1')
    assert.equal(document.members.length, 1)
    assert.ok(!exported.stdout.includes(desktop.token), 'the export holds a device token')
    assert.ok(!exported.stdout.includes(pairing.code), 'the export holds a pairing code')
    assert.ok(!exported.stdout.includes(ownerPassword), 'the export holds a password')
    assert.ok(!exported.stdout.includes(ownerSecret), 'the export holds the TOTP secret')
  })

  await check('restore rebuilds the instance in a second directory', async () => {
    const port = await freePort()
    const result = cli(binary, restoredDir, port, ['restore', backupFile])
    assert.equal(result.status, 0, result.stderr)
    const verified = cli(binary, restoredDir, port, ['check'])
    assert.equal(verified.status, 0, verified.stderr)
    assert.match(verified.stdout, /passed its checks/)
  })

  const requestsBeforeRestore = feed.requests.length
  const restored = await startServer(binary, restoredDir)
  await check('the restored server keeps the identity, the owner and the revocation', async () => {
    const response = expectStatus(await request(restored, 'GET', '/v1/instance'), 200, 'restored instance')
    assert.equal(response.json.instanceId, instance.instanceId)
    assert.equal(response.json.fingerprint, instance.fingerprint)
    assert.equal(response.json.setupRequired, false)
    assert.ok(!/setup#token=/.test(restored.log()), 'the restored server logged a first-run setup link')
    expectError(await request(restored, 'GET', '/v1/desktop/status', { headers: authorization }), 401, 'not_authenticated', 'revoked token on the restored server')
    await clearOfPeriodEdge()
    const session = browser(restored)
    const login = await session.call('POST', '/v1/owner/login', { json: { name: ownerName, password: ownerPassword, code: totp(ownerSecret, 30) } })
    expectStatus(login, 200, 'owner login on the restored server')
    session.adopt(login)
    const audit = expectStatus(await session.call('GET', '/v1/owner/audit'), 200, 'restored audit')
    assert.equal(audit.json.chain.ok, true)
    const members = expectStatus(await session.call('GET', '/v1/owner/members'), 200, 'restored members')
    assert.equal(members.json.members[0].name, memberName)
    const restoredUpdates = expectStatus(await session.call('GET', '/v1/owner/updates'), 200, 'restored updates')
    assert.equal(restoredUpdates.json.configured, false)
    assert.equal(restoredUpdates.json.error, 'not_configured')
    assert.equal(restoredUpdates.json.latest, null)
    assert.equal(feed.requests.length, requestsBeforeRestore, 'a server without a pinned key called the feed')
  })
  await restored.stop()

  await check('owner reset prints a one-time link without the server running', async () => {
    const port = await freePort()
    const result = cli(binary, restoredDir, port, ['owner', 'reset', ownerName])
    assert.equal(result.status, 0, result.stderr)
    assert.match(result.stdout, new RegExp(`http://localhost:${port}/setup#token=[A-Za-z0-9_-]{20,}`))
    const missing = cli(binary, restoredDir, port, ['owner', 'reset', 'nobody'])
    assert.notEqual(missing.status, 0)
  })

  await check('restore and check refuse a damaged archive', async () => {
    const damaged = join(workDir, 'backup', 'damaged.tar')
    const bytes = readFileSync(backupFile)
    bytes[Math.floor(bytes.length / 2)] ^= 0xff
    writeFileSync(damaged, bytes)
    const port = await freePort()
    const result = cli(binary, join(workDir, 'never'), port, ['restore', damaged])
    assert.notEqual(result.status, 0, 'restore accepted a damaged archive')
    const checked = cli(binary, join(workDir, 'never'), port, ['check', damaged])
    assert.notEqual(checked.status, 0, 'check accepted a damaged archive')
  })

  await check('configuration that would expose sign-in tokens is refused at start', async () => {
    const port = await freePort()
    const insecure = run(binary, ['serve'], { env: serverEnvironment(join(workDir, 'refused'), port, { SESAME_PUBLIC_URL: 'http://sesame.example.net' }), stdio: ['ignore', 'pipe', 'pipe'], timeout: 15000 })
    assert.equal(insecure.status, 1)
    assert.match(insecure.stderr, /https/)
    const wideProxy = run(binary, ['serve'], { env: serverEnvironment(join(workDir, 'refused'), port, { SESAME_TRUSTED_PROXIES: '0.0.0.0/0' }), stdio: ['ignore', 'pipe', 'pipe'], timeout: 15000 })
    assert.equal(wideProxy.status, 1)
    assert.match(wideProxy.stderr, /loopback, private, or link-local/)
    assert.equal(existsSync(join(workDir, 'refused')), false, 'a refused start still created a data directory')
  })
}
