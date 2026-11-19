import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { randomBytes } from 'node:crypto'
import { mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync } from 'node:fs'
import net from 'node:net'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { connect, request, sleep } from './selfhost-client.mjs'
import { repositoryRoot } from './selfhost-doc.mjs'
import { mustRun, run, waitUntil } from './selfhost-docker.mjs'
import { redactSensitiveLog } from './selfhost-recovery.mjs'

export { repositoryRoot }

const migrationDirectory = ['internal', 'accounts', 'migrations']
const legalSource = ['internal', 'httpapi', 'auth_routes.go']
const releaseTagPattern = /^v\d+\.\d+\.\d+$/

export function redact(text) {
  return redactSensitiveLog(text).replace(/((?:token|invite)=)[A-Za-z0-9_%-]+/g, '$1[redacted]')
}

export function describeFailure(error) {
  const text = String(error?.stack ?? error)
  const named = error?.message ? String(error.message).split('\n', 1)[0].slice(0, 300) : ''
  return redact(named && !text.includes(named) ? `${named}\n${text}` : text)
}

export function databaseServerUrl() {
  const value = process.env.SESAME_TEST_DATABASE_URL
  if (!value) {
    throw new Error('Set SESAME_TEST_DATABASE_URL to a throwaway PostgreSQL server first. This check creates and drops a database of its own on it and never skips.')
  }
  return value
}

export function reporter() {
  const state = { passed: 0 }
  state.check = async (name, body) => {
    try {
      const result = await body()
      state.passed += 1
      console.log(`ok - ${name}`)
      return result
    } catch (error) {
      error.message = `${name}: ${error.message}`
      throw error
    }
  }
  return state
}

export function legalVersions(sourceRoot) {
  const source = readFileSync(join(sourceRoot, ...legalSource), 'utf8')
  const read = (name) => {
    const match = source.match(new RegExp(`${name}\\s*=\\s*"([^"]+)"`))
    assert.ok(match, `${name} is not defined in ${legalSource.join('/')}`)
    return match[1]
  }
  return { terms: read('termsVersion'), privacy: read('privacyVersion') }
}

export function migrationNames(sourceRoot) {
  return readdirSync(join(sourceRoot, ...migrationDirectory)).filter((name) => name.endsWith('.sql')).sort()
}

export function newestReleaseTag(repository) {
  const listed = mustRun('git', ['tag', '--list', '--sort=-v:refname'], { cwd: repository }).stdout.split('\n').map((tag) => tag.trim())
  const tag = listed.find((candidate) => releaseTagPattern.test(candidate))
  if (!tag) {
    throw new Error('No release tag such as v1.2.3 exists in this checkout, so there is no earlier release to upgrade from. Fetch the tags and try again.')
  }
  return tag
}

export function exportSource(repository, ref, destination) {
  mkdirSync(destination, { recursive: true })
  const archive = `${destination}.tar`
  mustRun('git', ['archive', '--format=tar', '--output', archive, ref], { cwd: repository })
  mustRun('tar', ['-x', '-f', archive, '-C', destination])
  rmSync(archive, { force: true })
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

function parseMessage(envelopeTo, raw) {
  const [head, ...rest] = raw.split('\r\n\r\n')
  const subject = head.match(/^Subject: (.*)$/m)?.[1] ?? ''
  return { to: envelopeTo, subject, text: rest.join('\r\n\r\n') }
}

export async function startMailbox() {
  const messages = []
  const sockets = new Set()
  const server = net.createServer((socket) => {
    sockets.add(socket)
    socket.on('close', () => sockets.delete(socket))
    socket.on('error', () => {})
    let buffer = ''
    let recipient = ''
    let collecting = false
    socket.write('220 capture ready\r\n')
    socket.on('data', (chunk) => {
      buffer += chunk.toString('utf8')
      for (;;) {
        if (collecting) {
          const end = buffer.indexOf('\r\n.\r\n')
          if (end < 0) return
          const raw = buffer.slice(0, end).replace(/\r\n\.\./g, '\r\n.')
          buffer = buffer.slice(end + 5)
          collecting = false
          messages.push(parseMessage(recipient, raw))
          socket.write('250 queued\r\n')
          continue
        }
        const lineEnd = buffer.indexOf('\r\n')
        if (lineEnd < 0) return
        const line = buffer.slice(0, lineEnd)
        buffer = buffer.slice(lineEnd + 2)
        const verb = line.split(' ', 1)[0].toUpperCase()
        if (verb === 'RCPT') recipient = line.match(/<([^>]*)>/)?.[1] ?? ''
        if (verb === 'DATA') {
          collecting = true
          socket.write('354 go ahead\r\n')
        } else if (verb === 'QUIT') {
          socket.end('221 bye\r\n')
        } else {
          socket.write('250 ok\r\n')
        }
      }
    })
  })
  await new Promise((resolveListen, reject) => {
    server.once('error', reject)
    server.listen(0, '127.0.0.1', resolveListen)
  })
  const { port } = server.address()
  return {
    address: `127.0.0.1:${port}`,
    messages,
    waitFor: (to, subject, seconds) => waitUntil(`the ${subject} email for ${to}`, () => messages.find((message) => message.to === to && message.subject === subject), { seconds, interval: 250 }),
    async close() {
      for (const socket of sockets) socket.destroy()
      await new Promise((resolveClose) => server.close(resolveClose))
    },
  }
}

export async function startStack({ label }) {
  const databaseUrl = databaseServerUrl()
  const root = mkdtempSync(join(tmpdir(), `sesame-hosted-${label}-`))
  const databaseName = `sesame_hosted_${label}_${process.pid}_${Date.now()}`
  const port = await freePort()
  const mailbox = await startMailbox()
  const secrets = {
    capabilitySeed: randomBytes(32).toString('base64url'),
    adminKey: randomBytes(32).toString('hex'),
    adminPepper: randomBytes(32).toString('hex'),
    syncPepper: randomBytes(32).toString('hex'),
  }
  const servers = []
  let helper = ''
  let databaseCreated = false
  let databaseUrlOwn = ''

  const baseEnvironment = () => Object.fromEntries(Object.entries(process.env).filter(([name]) => !name.startsWith('SESAME_') && name !== 'DATABASE_URL'))
  const origins = { web: `http://localhost:${port}`, admin: `http://127.0.0.1:${port}` }
  const apiEnvironment = () => ({
    ...baseEnvironment(),
    DATABASE_URL: databaseUrlOwn,
    SESAME_API_ADDR: `127.0.0.1:${port}`,
    SESAME_WEB_ORIGIN: origins.web,
    SESAME_ADMIN_ORIGIN: origins.admin,
    SESAME_SESSION_SECURE: 'false',
    SESAME_REGISTRATION_MODE: 'invite',
    SESAME_CAPABILITY_SIGNING_KEY: secrets.capabilitySeed,
    SESAME_ADMIN_ENCRYPTION_KEY: secrets.adminKey,
    SESAME_ADMIN_IP_PEPPER: secrets.adminPepper,
    SESAME_SYNC_OWNER_PEPPER: secrets.syncPepper,
    SESAME_ENV: 'development',
    SESAME_SMTP_ADDR: mailbox.address,
    SESAME_SMTP_FROM: 'Sesame <accounts@example.com>',
    SESAME_SMTP_ALLOW_INSECURE_LOCAL: 'true',
  })

  const stack = {
    root,
    origins,
    mailbox,
    secrets,
    instance: connect(`http://127.0.0.1:${port}`),
    build(sourceRoot, name) {
      const output = join(root, 'bin', name)
      mkdirSync(output, { recursive: true })
      const binaries = {}
      for (const command of ['api', 'migrate', 'adminctl']) {
        binaries[command] = join(output, command)
        mustRun('go', ['build', '-trimpath', '-o', binaries[command], `./cmd/${command}`], { cwd: sourceRoot, env: { ...process.env, CGO_ENABLED: '0' } })
      }
      return binaries
    },
    buildDatabaseHelper(sourceRoot) {
      helper = join(root, 'bin', 'scratchdb')
      mustRun('go', ['build', '-trimpath', '-o', helper, './internal/testdb/scratchdb'], { cwd: sourceRoot, env: { ...process.env, CGO_ENABLED: '0' } })
    },
    createDatabase() {
      assert.ok(helper, 'build the database helper first')
      databaseUrlOwn = mustRun(helper, ['create', databaseName], { env: { ...process.env, SESAME_TEST_DATABASE_URL: databaseUrl } }).stdout.trim()
      databaseCreated = true
    },
    migrate(binaries) {
      const result = run(binaries.migrate, [], { env: { ...baseEnvironment(), DATABASE_URL: databaseUrlOwn } })
      assert.equal(result.status, 0, `migrate exited ${result.status}\n${redact(result.stderr)}`)
      assert.match(`${result.stdout}${result.stderr}`, /migrations applied successfully/)
    },
    adminctl(binaries, args) {
      const result = run(binaries.adminctl, args, { env: apiEnvironment() })
      assert.equal(result.status, 0, `adminctl ${args[0]} exited ${result.status}\n${redact(result.stderr)}`)
      return result.stdout
    },
    async startApi(binaries, { expectExit = false } = {}) {
      const child = spawn(binaries.api, [], { env: apiEnvironment(), stdio: ['ignore', 'pipe', 'pipe'] })
      let output = ''
      child.stdout.on('data', (chunk) => { output += chunk })
      child.stderr.on('data', (chunk) => { output += chunk })
      const exited = new Promise((resolveExit) => child.once('exit', (code, signal) => resolveExit({ code, signal })))
      const server = {
        log: () => output,
        exited,
        async stop() {
          if (child.exitCode === null && child.signalCode === null) child.kill('SIGTERM')
          return exited
        },
      }
      servers.push(server)
      const deadline = Date.now() + 30000
      while (Date.now() < deadline) {
        if (child.exitCode !== null) {
          if (expectExit) return server
          throw new Error(`the API exited early with code ${child.exitCode}\n${redact(output)}`)
        }
        const ready = await request(stack.instance, 'GET', '/readyz', { timeout: 2000 }).catch(() => null)
        if (ready?.status === 200) {
          assert.equal(expectExit, false, 'the API started although it was expected to refuse')
          return server
        }
        await sleep(100)
      }
      throw new Error(`the API was not ready within 30 seconds\n${redact(output)}`)
    },
    async close() {
      for (const server of servers) await server.stop().catch(() => {})
      await mailbox.close().catch(() => {})
      if (databaseCreated) run(helper, ['drop', databaseName], { env: { ...process.env, SESAME_TEST_DATABASE_URL: databaseUrl } })
      rmSync(root, { recursive: true, force: true })
    },
    servers,
  }
  return stack
}

export function webBrowser(instance, origin, csrfPath) {
  const cookies = new Map()
  let csrf = ''
  const call = async (method, path, { json, headers = {} } = {}) => {
    const outgoing = { origin, ...headers }
    if (cookies.size > 0) outgoing.cookie = [...cookies].map(([name, value]) => `${name}=${value}`).join('; ')
    if (csrf && method !== 'GET') outgoing['x-sesame-csrf'] = csrf
    const response = await request(instance, method, path, { json, headers: outgoing })
    for (const set of [].concat(response.headers['set-cookie'] ?? [])) {
      const [pair] = set.split(';')
      const separator = pair.indexOf('=')
      const name = pair.slice(0, separator)
      const value = pair.slice(separator + 1)
      if (value === '' || /;\s*Max-Age=0/i.test(set)) cookies.delete(name)
      else cookies.set(name, value)
    }
    return response
  }
  return {
    call,
    cookieNames: () => [...cookies.keys()],
    async protect() {
      const response = await call('GET', csrfPath)
      assert.equal(response.status, 200, `${csrfPath} returned ${response.status}`)
      csrf = response.json.token
      assert.ok(csrf, `${csrfPath} returned no token`)
    },
  }
}
