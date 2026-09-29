import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { readFileSync } from 'node:fs'
import { mkdtemp, readFile, readdir, rm, stat, writeFile } from 'node:fs/promises'
import { dirname, join } from 'node:path'
import { tmpdir } from 'node:os'
import { fileURLToPath } from 'node:url'
import { gzipSync } from 'node:zlib'
import { randomBytes } from 'node:crypto'
import { Readable } from 'node:stream'
import { gunzipSync } from 'node:zlib'
import test from 'node:test'
import { fileIO } from './deploy-io.mjs'
import {
  assertUsableBackup,
  classifyDeployment,
  compareVersions,
  deployRelease,
  parseRelease,
  readDeployedState,
  rehearsalEnvFile,
  rehearsalRoleBootstrapSql,
  rewriteEnvImages,
  rollbackRelease,
  writeCompressedBackup,
} from './deploy-release-lib.mjs'

const COMMIT = 'a'.repeat(40)
const DIGEST = `sha256:${'b'.repeat(64)}`
const DIGEST2 = `sha256:${'c'.repeat(64)}`
const ENV_TEXT = [
  'SESAME_DATABASE_PASSWORD=secret-database',
  'SESAME_API_IMAGE=registry.test.invalid/api@sha256:' + '9'.repeat(64),
  'SESAME_TRUSTED_PROXIES=10.0.0.0/8',
  'SESAME_ACCOUNT_IMAGE=registry.test.invalid/account@sha256:' + '9'.repeat(64),
  'SESAME_ADMIN_IMAGE=registry.test.invalid/admin@sha256:' + '9'.repeat(64),
  'SESAME_CAPABILITY_SIGNING_KEY=secret-capability',
].join('\n')

function backupPayload() {
  return Buffer.concat([Buffer.from('-- PostgreSQL database dump\n'), randomBytes(4096)])
}

function manifest(version, digest = DIGEST) {
  return JSON.stringify({
    schemaVersion: 1,
    version,
    commit: COMMIT,
    images: {
      api: `registry.test.invalid/api@${digest}`,
      account: `registry.test.invalid/account@${digest}`,
      admin: `registry.test.invalid/admin@${digest}`,
    },
  })
}

function releaseOf(version, digest) {
  return parseRelease(manifest(version, digest))
}

const PROD_ENV = '/host/deploy/compose/.env.production'

function fakeIO(overrides = {}) {
  const files = new Map()
  const calls = { pull: 0, backup: 0, rehearse: 0, migrations: 0, candidate: 0, switch: 0, up: 0, live: 0 }
  const io = {
    files,
    calls,
    async readText(path) {
      const value = files.get(path)
      if (value === undefined) {
        const error = new Error('missing file')
        error.code = 'ENOENT'
        throw error
      }
      return value.toString('utf8')
    },
    async writeText(path, text) { files.set(path, Buffer.from(text)) },
    async writeBinary(path, bytes) { files.set(path, bytes) },
    async writeJSONAtomic(path, value) { files.set(path, Buffer.from(JSON.stringify(value, null, 2))) },
    async renamePath(from, to) {
      const value = files.get(from)
      if (value === undefined) {
        const error = new Error('missing file')
        error.code = 'ENOENT'
        throw error
      }
      files.set(to, value)
      files.delete(from)
    },
    async exists(path) { return files.has(path) },
    async mkdirp() {},
    async unlink(path) { files.delete(path) },
    async pullImage() { calls.pull += 1 },
    async inspectImage(reference) {
      const digest = reference.split('@')[1]
      return { repoDigests: [`${reference.split('@')[0]}@${digest}`], labels: { 'org.opencontainers.image.version': '1.1.0', 'org.opencontainers.image.revision': COMMIT } }
    },
    async takeBackup(path) {
      calls.backup += 1
      const compressed = gzipSync(backupPayload())
      files.set(path, compressed)
      return { sha256: createHash('sha256').update(compressed).digest('hex'), bytes: compressed.length }
    },
    async rehearse() { calls.rehearse += 1; return { ok: true } },
    async runMigrations() { calls.migrations += 1; return { ok: true } },
    async candidateHealth() { calls.candidate += 1; return { ok: true, version: '1.1.0', commit: COMMIT } },
    async switchTraffic(stagingPath) {
      calls.switch += 1
      const value = files.get(stagingPath)
      if (value === undefined) {
        const error = new Error('missing file')
        error.code = 'ENOENT'
        throw error
      }
      files.set(PROD_ENV, value)
      files.delete(stagingPath)
      return { ok: true }
    },
    async composeUp() { calls.up += 1; return { ok: true } },
    async liveHealth() {
      calls.live += 1
      return calls.live === 1 ? { ok: false, error: 'nothing is serving' } : { ok: true, version: '1.1.0', commit: COMMIT }
    },
    now: () => '2026-09-07T00:00:00.000Z',
  }
  return Object.assign(io, overrides)
}

function seedEnvironment(io, { current = null, history = [], pending = null } = {}) {
  io.files.set(PROD_ENV, Buffer.from(ENV_TEXT))
  if (current || history.length > 0) io.files.set('/state/deployed.json', Buffer.from(JSON.stringify({ schemaVersion: 1, current, history })))
  if (pending) io.files.set('/state/pending.json', Buffer.from(JSON.stringify(pending)))
}

const env = { root: '/state', prodEnvPath: PROD_ENV }

test('parses the release manifest and binds it to its exact bytes', () => {
  const release = releaseOf('1.1.0')
  assert.equal(release.version, '1.1.0')
  assert.equal(release.commit, COMMIT)
  assert.equal(release.setDigest, createHash('sha256').update(manifest('1.1.0')).digest('hex'))
  assert.equal(release.images.api.digest, DIGEST)
})

test('parses the workflow-generated manifest shape with named image objects', () => {
  const image = (name, digest) => ({ reference: `${name}@${digest}`, name, digest })
  const workflowManifest = (version, digest) => JSON.stringify({
    schemaVersion: 1,
    version,
    commit: COMMIT,
    images: {
      api: image('registry.test.invalid/api', digest),
      account: image('registry.test.invalid/account', digest),
      admin: image('registry.test.invalid/admin', digest),
    },
  })
  const release = parseRelease(workflowManifest('1.1.0', DIGEST))
  assert.equal(release.version, '1.1.0')
  assert.equal(release.commit, COMMIT)
  assert.equal(release.images.api.reference, `registry.test.invalid/api@${DIGEST}`)
  assert.equal(release.images.api.digest, DIGEST)
  assert.equal(release.images.account.digest, DIGEST)
  assert.equal(release.images.admin.digest, DIGEST)
  assert.equal(release.setDigest, createHash('sha256').update(workflowManifest('1.1.0', DIGEST)).digest('hex'))
})

test('rejects malformed manifests', () => {
  for (const broken of [
    '{"schemaVersion":2,"version":"1.0.0","commit":"' + COMMIT + '","images":{}}',
    '{"schemaVersion":1,"version":"1.0","commit":"' + COMMIT + '","images":{}}',
    '{"schemaVersion":1,"version":"1.0.0","commit":"abc","images":{}}',
    '{"schemaVersion":1,"version":"1.0.0","commit":"' + COMMIT + '","images":{"api":"registry.test.invalid/api:1.0.0","account":"registry.test.invalid/account@' + DIGEST + '","admin":"registry.test.invalid/admin@' + DIGEST + '"}}',
    '{"schemaVersion":1,"version":"1.0.0","commit":"' + COMMIT + '","images":{"api":"registry.test.invalid/api@' + DIGEST + '","account":"registry.test.invalid/account@' + DIGEST + '"}}',
    '{"schemaVersion":1,"version":"1.0.0","commit":"' + COMMIT + '","images":{"api":{"name":"registry.test.invalid/api","digest":"' + DIGEST + '"},"account":"registry.test.invalid/account@' + DIGEST + '","admin":"registry.test.invalid/admin@' + DIGEST + '"}}',
  ]) {
    assert.throws(() => parseRelease(broken))
  }
})

test('orders versions including prereleases', () => {
  assert.equal(compareVersions('1.2.3', '1.10.0'), -1)
  assert.equal(compareVersions('2.0.0', '2.0.0'), 0)
  assert.equal(compareVersions('2.0.0-rc.1', '2.0.0'), -1)
  assert.equal(compareVersions('1.0.0-alpha', '1.0.0-beta'), -1)
  assert.equal(compareVersions('1.0.0-1', '1.0.0-alpha'), -1)
  assert.equal(compareVersions('1.0.0-rc.2', '1.0.0-rc.10'), -1)
})

test('rewrites only the three image lines and refuses incomplete files', () => {
  const rewritten = rewriteEnvImages(ENV_TEXT, {
    api: { reference: 'registry.test.invalid/api@' + DIGEST },
    account: { reference: 'registry.test.invalid/account@' + DIGEST },
    admin: { reference: 'registry.test.invalid/admin@' + DIGEST },
  })
  assert.ok(rewritten.includes('SESAME_API_IMAGE=registry.test.invalid/api@' + DIGEST))
  assert.ok(rewritten.includes('SESAME_DATABASE_PASSWORD=secret-database'))
  assert.ok(rewritten.includes('SESAME_TRUSTED_PROXIES=10.0.0.0/8'))
  assert.ok(rewritten.includes('SESAME_CAPABILITY_SIGNING_KEY=secret-capability'))
  const missing = ENV_TEXT.replace(/SESAME_ADMIN_IMAGE=.*\n/, '')
  assert.throws(() => rewriteEnvImages(missing, { api: {}, account: {}, admin: {} }))
  const duplicated = ENV_TEXT.replace(/SESAME_TRUSTED_PROXIES=10\.0\.0\.0\/8\n/, 'SESAME_API_IMAGE=x\n')
  assert.throws(() => rewriteEnvImages(duplicated, { api: {}, account: {}, admin: {} }))
})

test('accepts only a usable gzip dump as the pre-deployment backup', async () => {
  const sha256 = await assertUsableBackup(gzipSync(backupPayload()))
  assert.match(sha256, /^[0-9a-f]{64}$/)
  await assert.rejects(() => assertUsableBackup(Buffer.from('plain text')))
  await assert.rejects(() => assertUsableBackup(gzipSync(Buffer.from('too small'))))
  await assert.rejects(() => assertUsableBackup(gzipSync(Buffer.from('deflate garbage without the dump marker'.repeat(64)))))
})

test('streams a backup to a private file with a matching digest', async () => {
  const directory = await mkdtemp(join(tmpdir(), 'sesame-stream-backup-'))
  try {
    const file = join(directory, 'backup.sql.gz')
    const payload = backupPayload()
    const backup = await writeCompressedBackup(Readable.from([payload.subarray(0, 19), payload.subarray(19)]), file)
    const compressed = await readFile(file)
    assert.deepEqual(gunzipSync(compressed), payload)
    assert.equal(backup.bytes, compressed.length)
    assert.equal(backup.sha256, createHash('sha256').update(compressed).digest('hex'))
    assert.equal((await stat(file)).mode & 0o777, 0o600)
    assert.deepEqual(await readdir(directory), ['backup.sql.gz'])
  } finally {
    await rm(directory, { recursive: true, force: true })
  }
})

test('refuses malformed and interrupted streamed backups without publishing a file', async () => {
  const directory = await mkdtemp(join(tmpdir(), 'sesame-stream-backup-fail-'))
  const file = join(directory, 'backup.sql.gz')
  try {
    await assert.rejects(() => writeCompressedBackup(Readable.from([randomBytes(4096)]), file), /does not contain a PostgreSQL dump/)
    assert.deepEqual(await readdir(directory), [])

    async function* interrupted() {
      yield backupPayload().subarray(0, 1024)
      throw new Error('dump interrupted')
    }
    await assert.rejects(() => writeCompressedBackup(Readable.from(interrupted()), file), /dump interrupted/)
    assert.deepEqual(await readdir(directory), [])

    await assert.rejects(() => writeCompressedBackup(Readable.from([backupPayload()]), file, async () => { throw new Error('dump exited with an error') }), /dump exited with an error/)
    assert.deepEqual(await readdir(directory), [])

    await writeFile(file, 'previous backup')
    await assert.rejects(() => writeCompressedBackup(Readable.from([backupPayload()]), file), { code: 'EEXIST' })
    assert.equal(await readFile(file, 'utf8'), 'previous backup')
    assert.deepEqual(await readdir(directory), ['backup.sql.gz'])
  } finally {
    await rm(directory, { recursive: true, force: true })
  }
})

test('classifies deployments', () => {
  const release = releaseOf('1.1.0')
  assert.deepEqual(classifyDeployment({ current: null, history: [] }, release), { action: 'bootstrap' })
  assert.deepEqual(classifyDeployment({ current: { version: '1.1.0', setDigest: release.setDigest }, history: [] }, release), { action: 'noop' })
  assert.deepEqual(classifyDeployment({ current: { version: '1.1.0', setDigest: 'other' }, history: [] }, release), { action: 'conflict' })
  assert.deepEqual(classifyDeployment({ current: { version: '2.0.0', setDigest: 'other' }, history: [] }, release), { action: 'stale' })
  assert.deepEqual(classifyDeployment({ current: { version: '1.0.0', setDigest: 'other' }, history: [] }, release), { action: 'deploy' })
})

test('bootstraps a first deployment with a backup and a reusable env snapshot', async () => {
  const io = fakeIO()
  seedEnvironment(io)
  const release = releaseOf('1.1.0')
  const result = await deployRelease(io, { ...env, release })
  assert.equal(result.deployed, '1.1.0')
  assert.equal(io.calls.backup, 1)
  assert.equal(io.calls.rehearse, 1)
  assert.equal(io.calls.migrations, 1)
  assert.equal(io.calls.candidate, 1)
  assert.equal(io.calls.switch, 1)
  const state = await readDeployedState(io, '/state')
  assert.equal(state.current.version, '1.1.0')
  assert.equal(state.current.setDigest, release.setDigest)
  assert.equal(state.current.backup.bytes > 1024, true)
  assert.equal(state.history.length, 1)
  assert.equal(state.history[0].action, 'bootstrap')
  assert.ok(io.files.get('/state/history/pre-bootstrap/env.production'))
  assert.ok(!io.files.has('/state/pending.json'))
  const liveEnv = (await io.readText(PROD_ENV)).split('\n').find((line) => line.startsWith('SESAME_API_IMAGE='))
  assert.equal(liveEnv, `SESAME_API_IMAGE=${release.images.api.reference}`)
})

test('deploys over a previous revision and records the rollback anchor', async () => {
  const io = fakeIO()
  const previous = { version: '1.0.0', commit: COMMIT, setDigest: 'old-digest', images: releaseOf('1.0.0', DIGEST2).images, deployedAt: '2026-09-01T00:00:00.000Z', backup: { file: '/state/backups/old.sql.gz', sha256: 'f'.repeat(64), bytes: 4096 }, previous: null }
  seedEnvironment(io, { current: previous, history: [{ action: 'bootstrap', version: '1.0.0', commit: COMMIT, setDigest: 'old-digest', images: previous.images, from: null, at: '2026-09-01T00:00:00.000Z' }] })
  io.files.set('/state/history/1.0.0/env.production', Buffer.from(ENV_TEXT))
  const release = releaseOf('1.1.0')
  let liveCalls = 0
  io.liveHealth = async () => {
    liveCalls += 1
    return liveCalls === 1 ? { ok: true, version: '1.0.0', commit: COMMIT } : { ok: true, version: '1.1.0', commit: COMMIT }
  }
  const result = await deployRelease(io, { ...env, release })
  assert.equal(result.from, '1.0.0')
  const state = await readDeployedState(io, '/state')
  assert.deepEqual(state.current.previous, { version: '1.0.0', setDigest: 'old-digest', images: previous.images })
  assert.equal(state.history.at(-1).action, 'deploy')
  assert.equal(state.history.at(-1).from, '1.0.0')
  assert.ok(io.files.get('/state/history/1.0.0/env.production'))
})

test('a noop deploy converges a drifted pinned env to the release digests', async () => {
  const release = releaseOf('1.1.0')
  const current = { version: '1.1.0', commit: COMMIT, setDigest: release.setDigest, images: release.images, deployedAt: '', previous: null }
  const io = fakeIO()
  seedEnvironment(io, { current, history: [] })
  io.files.set(PROD_ENV, Buffer.from(ENV_TEXT))
  io.composeUp = async () => ({ ok: true })
  io.liveHealth = async () => ({ ok: true, version: '1.1.0', commit: COMMIT })
  await assert.rejects(() => deployRelease(io, { ...env, release }), /already the deployed revision/)
  assert.ok(io.files.get(PROD_ENV).toString().includes(`SESAME_API_IMAGE=${release.images.api.reference}`))
})

test('a deploy whose target already serves converges the pinned env before recording', async () => {
  const io = fakeIO()
  seedEnvironment(io, {})
  io.files.set(PROD_ENV, Buffer.from(ENV_TEXT))
  const release = releaseOf('1.1.0')
  io.composeUp = async () => ({ ok: true })
  io.liveHealth = async () => ({ ok: true, version: '1.1.0', commit: COMMIT })
  await deployRelease(io, { ...env, release })
  const state = await readDeployedState(io, '/state')
  assert.equal(state.current.version, '1.1.0')
  assert.ok(io.files.get(PROD_ENV).toString().includes(`SESAME_API_IMAGE=${release.images.api.reference}`))
})

test('refuses to redeploy the same revision, an older one, or a changed manifest', async () => {
  const release = releaseOf('1.1.0')
  const current = { version: '1.1.0', commit: COMMIT, setDigest: release.setDigest, images: release.images, deployedAt: '', previous: null }
  const same = fakeIO()
  seedEnvironment(same, { current, history: [] })
  same.liveHealth = async () => ({ ok: true, version: '1.1.0', commit: COMMIT })
  await assert.rejects(() => deployRelease(same, { ...env, release }), /already the deployed revision/)
  const older = releaseOf('1.0.9')
  await assert.rejects(() => deployRelease(same, { ...env, release: older }), /older than the deployed/)
  const changed = { ...current, setDigest: 'different' }
  const conflicting = fakeIO()
  seedEnvironment(conflicting, { current: changed, history: [] })
  await assert.rejects(() => deployRelease(conflicting, { ...env, release }), /different manifest/)
})

test('refuses a concurrent pending deployment from a different manifest', async () => {
  const io = fakeIO()
  const pending = { schemaVersion: 1, phase: 'prepared', release: { ...releaseOf('2.0.0'), images: releaseOf('2.0.0').images }, startedAt: '' }
  seedEnvironment(io, { pending })
  await assert.rejects(() => deployRelease(io, { ...env, release: releaseOf('1.1.0') }), /still pending/)
})

test('refuses images whose local digest or identity does not match', async () => {
  const io = fakeIO()
  seedEnvironment(io)
  const release = releaseOf('1.1.0')
  io.inspectImage = async () => ({ repoDigests: ['registry.test.invalid/api@sha256:' + 'e'.repeat(64)], labels: {} })
  await assert.rejects(() => deployRelease(io, { ...env, release }), /digest/)
  io.inspectImage = async (reference) => ({ repoDigests: [reference], labels: { 'org.opencontainers.image.version': '0.9.0', 'org.opencontainers.image.revision': COMMIT } })
  await assert.rejects(() => deployRelease(io, { ...env, release }), /identity does not match/)
  assert.ok(!io.files.has('/state/pending.json'))
})

test('a failed rehearsal aborts before promotion and retries only the rehearsal', async () => {
  const io = fakeIO()
  seedEnvironment(io)
  let attempt = 0
  io.rehearse = async () => {
    attempt += 1
    io.calls.rehearse += 1
    return attempt === 1 ? { ok: false, error: 'candidate migration failed on restored data' } : { ok: true }
  }
  const release = releaseOf('1.1.0')
  await assert.rejects(() => deployRelease(io, { ...env, release }), /Migration rehearsal failed/)
  assert.equal(io.calls.backup, 1)
  assert.equal(io.calls.migrations, 0)
  const pending = JSON.parse(await io.readText('/state/pending.json'))
  assert.equal(pending.rehearsal.ok, false)
  await deployRelease(io, { ...env, release })
  assert.equal(io.calls.backup, 1)
  assert.equal(io.calls.rehearse, 2)
  assert.equal(io.calls.migrations, 1)
})

test('a failed candidate check leaves the traffic and env untouched', async () => {
  const io = fakeIO()
  seedEnvironment(io)
  io.candidateHealth = async () => ({ ok: false, error: 'readiness returned 503' })
  const release = releaseOf('1.1.0')
  await assert.rejects(() => deployRelease(io, { ...env, release }), /Candidate health check failed/)
  assert.equal(io.calls.switch, 0)
  assert.equal(await io.readText(PROD_ENV), ENV_TEXT)
  const pending = JSON.parse(await io.readText('/state/pending.json'))
  assert.equal(pending.phase, 'prepared')
  assert.equal(pending.rehearsal.ok, true)
})

test('a healthy candidate that reports the wrong revision aborts before the switch', async () => {
  const io = fakeIO()
  seedEnvironment(io)
  const release = releaseOf('1.1.0')
  io.candidateHealth = async () => ({ ok: true, version: '1.0.0', commit: COMMIT })
  await assert.rejects(() => deployRelease(io, { ...env, release }), /reports 1\.0\.0 at /)
  assert.equal(io.calls.switch, 0)
  assert.equal(await io.readText(PROD_ENV), ENV_TEXT)
  const pending = JSON.parse(await io.readText('/state/pending.json'))
  assert.equal(pending.phase, 'prepared')
})

test('the rehearsal env file carries the API environment and only the scratch database URL', () => {
  const scratchURL = 'postgres://sesame:scratch@127.0.0.1:5432/sesame?sslmode=disable'
  const text = rehearsalEnvFile({
    DATABASE_URL: 'postgres://production',
    SESAME_ADMIN_ENCRYPTION_KEY: 'secret-value',
    SESAME_SMTP_FROM: 'Sesame <accounts@example.test>',
    SESAME_EMPTY: null,
  }, scratchURL)
  assert.ok(text.includes('SESAME_ADMIN_ENCRYPTION_KEY=secret-value'))
  assert.ok(text.includes('SESAME_SMTP_FROM=Sesame <accounts@example.test>'))
  assert.ok(!text.includes('SESAME_EMPTY'))
  assert.equal(text.match(/DATABASE_URL=/g).length, 1)
  assert.ok(text.endsWith(`DATABASE_URL=${scratchURL}\n`))
  assert.throws(() => rehearsalEnvFile({ SESAME_BROKEN: 'line one\nline two' }, scratchURL), /spans multiple lines/)
})

test('the rehearsal passes the API environment through an env file, never argv', () => {
  const source = readFileSync(join(dirname(fileURLToPath(import.meta.url)), 'deploy-release.mjs'), 'utf8')
  assert.ok(source.includes('rehearsalEnvFile(apiEnvironment'))
  assert.ok(!/flatMap\(\(\[name, value\]\) => \['-e'/.test(source))
})

test('deploy files and their backup directory are written with restrictive permissions', async () => {
  const directory = await mkdtemp(join(tmpdir(), 'sesame-deploy-io-'))
  try {
    const backup = join(directory, 'backups', 'sesame.sql.gz')
    await fileIO.writeBinary(backup, Buffer.from('bytes'))
    assert.equal((await stat(backup)).mode & 0o777, 0o600)
    assert.equal((await stat(dirname(backup))).mode & 0o777, 0o700)
    const env = join(directory, 'state', 'rehearsal-api.env')
    await fileIO.writeText(env, 'SECRET=value\n')
    assert.equal((await stat(env)).mode & 0o777, 0o600)
  } finally {
    await rm(directory, { recursive: true, force: true })
  }
})

test('a failed traffic switch rolls back to the recorded snapshot', async () => {
  const io = fakeIO()
  const previousImages = releaseOf('1.0.0', DIGEST2).images
  const current = { version: '1.0.0', commit: COMMIT, setDigest: 'old-digest', images: previousImages, deployedAt: '', backup: { file: '/state/backups/old.sql.gz', sha256: 'f'.repeat(64), bytes: 4096 }, previous: null }
  seedEnvironment(io, { current, history: [{ action: 'bootstrap', version: '1.0.0', commit: COMMIT, setDigest: 'old-digest', images: previousImages, from: null, at: '', backup: current.backup }] })
  io.files.set('/state/history/1.0.0/env.production', Buffer.from(ENV_TEXT))
  const release = releaseOf('1.1.0')
  io.switchTraffic = async (stagingPath) => {
    io.files.set(PROD_ENV, io.files.get(stagingPath))
    io.files.delete(stagingPath)
    return { ok: false, error: 'compose up failed' }
  }
  io.liveHealth = async () => ({ ok: true, version: '1.0.0', commit: COMMIT })
  await assert.rejects(() => deployRelease(io, { ...env, release }), /1\.0\.0 is serving again/)
  assert.equal(await io.readText(PROD_ENV), ENV_TEXT)
  assert.equal(io.calls.up, 1)
  const state = await readDeployedState(io, '/state')
  assert.equal(state.current.version, '1.0.0')
  assert.equal(state.history.at(-1).action, 'rollback')
  assert.equal(state.history.at(-1).version, '1.0.0')
  assert.ok(!io.files.has('/state/pending.json'))
})

test('a live version mismatch after the switch triggers the same rollback', async () => {
  const io = fakeIO()
  const previousImages = releaseOf('1.0.0', DIGEST2).images
  const current = { version: '1.0.0', commit: COMMIT, setDigest: 'old-digest', images: previousImages, deployedAt: '', backup: { file: '/state/backups/old.sql.gz', sha256: 'f'.repeat(64), bytes: 4096 }, previous: null }
  seedEnvironment(io, { current, history: [{ action: 'bootstrap', version: '1.0.0', commit: COMMIT, setDigest: 'old-digest', images: previousImages, from: null, at: '', backup: current.backup }] })
  io.files.set('/state/history/1.0.0/env.production', Buffer.from(ENV_TEXT))
  const release = releaseOf('1.1.0')
  const live = [{ ok: true, version: '1.0.0', commit: COMMIT }, { ok: true, version: '1.1.0', commit: 'wrong' }, { ok: true, version: '1.0.0', commit: COMMIT }]
  io.liveHealth = async () => live.shift()
  await assert.rejects(() => deployRelease(io, { ...env, release }), /instead of 1\.1\.0 at /)
  const state = await readDeployedState(io, '/state')
  assert.equal(state.current.version, '1.0.0')
  assert.equal(state.history.at(-1).action, 'rollback')
})

test('an interrupted retry reuses the backup and rehearsal without duplicating them', async () => {
  const release = releaseOf('1.1.0')
  const pending = {
    schemaVersion: 1,
    phase: 'prepared',
    release: { version: release.version, commit: release.commit, setDigest: release.setDigest, images: release.images },
    startedAt: '2026-09-07T00:00:00.000Z',
    backup: { file: '/state/backups/sesame-1.1.0-20260907-000000.sql.gz', sha256: 'f'.repeat(64), bytes: 4096 },
    rehearsal: { ok: true, at: '2026-09-07T00:00:00.000Z' },
  }
  const io = fakeIO()
  seedEnvironment(io, { pending })
  io.files.set(pending.backup.file, Buffer.from('gzip-bytes'))
  const live = [{ ok: false, error: 'nothing is serving' }, { ok: true, version: '1.1.0', commit: COMMIT }]
  io.liveHealth = async () => live.shift()
  await deployRelease(io, { ...env, release })
  assert.equal(io.calls.backup, 0)
  assert.equal(io.calls.rehearse, 0)
  assert.equal(io.calls.switch, 1)
  const state = await readDeployedState(io, '/state')
  assert.equal(state.history.filter((entry) => entry.action === 'deploy' || entry.action === 'bootstrap').length, 1)
})

test('a crash after the traffic switch converges without a second switch', async () => {
  const release = releaseOf('1.1.0')
  const candidateEnv = ENV_TEXT
    .replace(/SESAME_API_IMAGE=.*/, `SESAME_API_IMAGE=${release.images.api.reference}`)
    .replace(/SESAME_ACCOUNT_IMAGE=.*/, `SESAME_ACCOUNT_IMAGE=${release.images.account.reference}`)
    .replace(/SESAME_ADMIN_IMAGE=.*/, `SESAME_ADMIN_IMAGE=${release.images.admin.reference}`)
  const pending = {
    schemaVersion: 1,
    phase: 'switching',
    release: { version: release.version, commit: release.commit, setDigest: release.setDigest, images: release.images },
    startedAt: '',
    backup: { file: '/state/backups/sesame-1.1.0.sql.gz', sha256: 'f'.repeat(64), bytes: 4096 },
    rehearsal: { ok: true, at: '' },
  }
  const io = fakeIO()
  seedEnvironment(io, { pending })
  io.files.set(PROD_ENV, Buffer.from(candidateEnv))
  io.liveHealth = async () => ({ ok: true, version: '1.1.0', commit: COMMIT })
  const result = await deployRelease(io, { ...env, release })
  assert.equal(result.deployed, '1.1.0')
  assert.equal(io.calls.switch, 0)
  assert.equal(io.calls.up, 0)
  const state = await readDeployedState(io, '/state')
  assert.equal(state.current.version, '1.1.0')
  assert.equal(state.history.at(-1).action, 'bootstrap')
})

test('rollback restores the previous revision and records it', async () => {
  const previousImages = releaseOf('1.0.0', DIGEST2).images
  const currentImages = releaseOf('1.1.0').images
  const current = { version: '1.1.0', commit: COMMIT, setDigest: 'new-digest', images: currentImages, deployedAt: '', backup: { file: '/state/backups/new.sql.gz', sha256: 'f'.repeat(64), bytes: 4096 }, previous: { version: '1.0.0', setDigest: 'old-digest', images: previousImages } }
  const history = [
    { action: 'bootstrap', version: '1.0.0', commit: COMMIT, setDigest: 'old-digest', images: previousImages, from: null, at: '', backup: { file: '/state/backups/old.sql.gz', sha256: 'f'.repeat(64), bytes: 4096 } },
    { action: 'deploy', version: '1.1.0', commit: COMMIT, setDigest: 'new-digest', images: currentImages, from: '1.0.0', at: '', backup: current.backup },
  ]
  const io = fakeIO()
  seedEnvironment(io, { current, history })
  io.files.set('/state/history/1.0.0/env.production', Buffer.from(ENV_TEXT))
  io.liveHealth = async () => ({ ok: true, version: '1.0.0', commit: COMMIT })
  const result = await rollbackRelease(io, { root: '/state', prodEnvPath: PROD_ENV })
  assert.deepEqual({ rolledBack: result.rolledBack, from: result.from }, { rolledBack: '1.0.0', from: '1.1.0' })
  assert.equal(await io.readText(PROD_ENV), ENV_TEXT)
  const state = await readDeployedState(io, '/state')
  assert.equal(state.current.version, '1.0.0')
  assert.deepEqual(state.current.previous, { version: '1.1.0', setDigest: 'new-digest', images: currentImages })
  assert.equal(state.history.at(-1).action, 'rollback')
})

test('rollback refuses unknown versions, missing snapshots, and a already-serving target', async () => {
  const current = { version: '1.1.0', commit: COMMIT, setDigest: 'new-digest', images: releaseOf('1.1.0').images, deployedAt: '', backup: null, previous: { version: '1.0.0', setDigest: 'old-digest', images: releaseOf('1.0.0', DIGEST2).images } }
  const io = fakeIO()
  seedEnvironment(io, { current, history: [{ action: 'bootstrap', version: '1.1.0', commit: COMMIT, setDigest: 'new-digest', images: current.images, from: null, at: '', backup: null }, { action: 'deploy', version: '1.0.0', commit: COMMIT, setDigest: 'old-digest', images: releaseOf('1.0.0', DIGEST2).images, from: null, at: '', backup: null }] })
  const inputs = { root: '/state', prodEnvPath: PROD_ENV }
  await assert.rejects(() => rollbackRelease(io, { ...inputs, targetVersion: '9.9.9' }), /never deployed by this tool/)
  await assert.rejects(() => rollbackRelease(io, { ...inputs, targetVersion: '1.0.0' }), /env snapshot for 1\.0\.0 is missing/)
  await assert.rejects(() => rollbackRelease(io, { ...inputs, targetVersion: '1.1.0' }), /already serving/)
  const nothing = fakeIO()
  seedEnvironment(nothing)
  await assert.rejects(() => rollbackRelease(nothing, inputs), /Nothing is recorded as deployed/)
})

test('the production stack connects each service through its own database role', () => {
  const root = join(dirname(fileURLToPath(import.meta.url)), '..')
  const read = (file) => readFileSync(join(root, 'deploy', 'compose', file), 'utf8')
  const production = read('compose.prod.yaml')
  assert.ok(production.includes('DATABASE_URL: postgres://sesame_owner:${SESAME_DATABASE_OWNER_PASSWORD'), 'production migrations do not use the owner role')
  assert.ok(production.includes('DATABASE_URL: postgres://sesame_app:${SESAME_DATABASE_APP_PASSWORD'), 'the production API does not use the application role')
  assert.ok(production.includes('POSTGRES_PASSWORD: ${SESAME_DATABASE_PASSWORD}'), 'production dropped the bootstrap superuser password')
  assert.ok(production.includes('./initdb:/docker-entrypoint-initdb.d:ro'), 'production does not mount the role init script')
  const development = read('compose.yaml')
  assert.ok(development.includes('DATABASE_URL: postgres://sesame_owner:${SESAME_DATABASE_OWNER_PASSWORD'), 'development migrations do not use the owner role')
  assert.ok(development.includes('DATABASE_URL: postgres://sesame_app:${SESAME_DATABASE_APP_PASSWORD'), 'the development API does not use the application role')
  const candidate = read('compose.candidate-check.yaml')
  assert.ok(candidate.includes('postgres://sesame_app:${SESAME_DATABASE_APP_PASSWORD'), 'the candidate check does not use the application role')
  const smoke = read('compose.release-smoke.yaml')
  assert.ok(smoke.includes('postgres://sesame_owner:sesame-smoke-owner'), 'the smoke migration does not use the owner role')
  assert.ok(smoke.includes('postgres://sesame_app:sesame-smoke-app'), 'the smoke API does not use the application role')
  assert.ok(smoke.includes('./initdb:/docker-entrypoint-initdb.d:ro'), 'the smoke database does not mount the role init script')
})

test('the role init script is idempotent and least privileged', async () => {
  const root = join(dirname(fileURLToPath(import.meta.url)), '..')
  const path = join(root, 'deploy', 'compose', 'initdb', '10-roles.sh')
  const script = readFileSync(path, 'utf8')
  assert.ok((await stat(path)).mode & 0o111, 'the role init script must be executable')
  for (const marker of [
    'IF NOT EXISTS (SELECT 1 FROM pg_roles',
    'NOSUPERUSER',
    'NOCREATEDB',
    'NOCREATEROLE',
    'GRANT CREATE, USAGE ON SCHEMA public TO sesame_owner',
    'GRANT pg_read_all_data TO sesame_backup',
    'ALTER FUNCTION %s OWNER TO sesame_owner',
  ]) {
    assert.ok(script.includes(marker), `the role init script is missing "${marker}"`)
  }
  assert.ok(!/PASSWORD\s*'/.test(script), 'the role init script must not hardcode a password')
})

test('the deploy tool dumps through the backup role', () => {
  const root = join(dirname(fileURLToPath(import.meta.url)), '..')
  const source = readFileSync(join(root, 'scripts', 'deploy-release.mjs'), 'utf8')
  assert.ok(source.includes(`'pg_dump', '-U', 'sesame_backup'`), 'the deploy tool does not dump through the backup role')
  assert.ok(!source.includes(`'pg_dump', '-U', 'sesame'`), 'the deploy tool still dumps through the bootstrap role')
})

test('the rehearsal creates the split database roles before restoring the backup', () => {
  const root = join(dirname(fileURLToPath(import.meta.url)), '..')
  const source = readFileSync(join(root, 'scripts', 'deploy-release.mjs'), 'utf8')
  const sql = rehearsalRoleBootstrapSql()
  for (const role of ['sesame_owner', 'sesame_app', 'sesame_backup']) {
    assert.ok(sql.includes(`CREATE ROLE ${role} LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;`), `the rehearsal does not create ${role}`)
  }
  assert.ok(sql.includes('GRANT pg_read_all_data TO sesame_backup;'), 'the rehearsal does not grant the backup role read access')
  assert.ok(!sql.includes('PASSWORD'), 'the rehearsal role bootstrap must not set role passwords')
  const bootstrapAt = source.indexOf('input: rehearsalRoleBootstrapSql()')
  const restoreAt = source.indexOf('restoring the backup into the rehearsal database failed')
  assert.ok(bootstrapAt >= 0, 'the rehearsal never creates the scratch database roles')
  assert.ok(restoreAt > bootstrapAt, 'the rehearsal restores the backup before the roles exist')
  assert.ok(source.includes(`'-v', 'ON_ERROR_STOP=1'`), 'the rehearsal restore does not stop on the first error')
})

test('the rehearsal role bootstrap matches the production role script', () => {
  const root = join(dirname(fileURLToPath(import.meta.url)), '..')
  const script = readFileSync(join(root, 'deploy', 'compose', 'initdb', '10-roles.sh'), 'utf8')
  const sql = rehearsalRoleBootstrapSql()
  for (const role of ['sesame_owner', 'sesame_app', 'sesame_backup']) {
    assert.ok(script.includes(`'${role}'`), `the production role script does not create ${role}`)
    assert.ok(sql.includes(`CREATE ROLE ${role} `), `the rehearsal bootstrap does not create ${role}`)
  }
  assert.ok(script.includes('GRANT pg_read_all_data TO sesame_backup'), 'the production role script does not grant the backup role read access')
  assert.ok(sql.includes('GRANT pg_read_all_data TO sesame_backup'), 'the rehearsal bootstrap does not grant the backup role read access')
})

const livePostgresImage = 'postgres:18-alpine@sha256:d3e1620b530c944afa6e887d22eb899824da68e19c52024bf98f5220c88a65b2'
const livePostgresSkip = spawnSync('docker', ['version', '--format', '{{.Server.Version}}'], { stdio: 'ignore' }).status !== 0
  ? 'docker is not available'
  : spawnSync('docker', ['image', 'inspect', livePostgresImage], { stdio: 'ignore' }).status !== 0
    ? 'the pinned rehearsal database image is not present'
    : false

test('the rehearsal roles let a role-split dump restore into a scratch database', { skip: livePostgresSkip, timeout: 180000 }, async () => {
  const suffix = `${process.pid}-${randomBytes(4).toString('hex')}`
  const production = `sesame-contract-production-${suffix}`
  const scratch = `sesame-contract-scratch-${suffix}`
  const password = randomBytes(12).toString('hex')
  const docker = (args, options = {}) => spawnSync('docker', args, { encoding: 'utf8', maxBuffer: 64 * 1024 * 1024, ...options })
  const psql = (container, input, database = 'sesame') => docker(['exec', '-i', container, 'psql', '-q', '-v', 'ON_ERROR_STOP=1', '-U', 'sesame', '-d', database], { input })
  try {
    for (const name of [production, scratch]) {
      const started = docker(['run', '-d', '--name', name, '-e', 'POSTGRES_USER=sesame', '-e', `POSTGRES_PASSWORD=${password}`, '-e', 'POSTGRES_DB=sesame', '--tmpfs', '/var/lib/postgresql', livePostgresImage])
      assert.equal(started.status, 0, `could not start ${name}: ${started.stderr}`)
    }
    for (const name of [production, scratch]) {
      let ready = false
      for (let attempt = 0; attempt < 90 && !ready; attempt += 1) {
        if (docker(['exec', name, 'pg_isready', '-q', '-U', 'sesame', '-d', 'sesame']).status === 0) {
          await new Promise((resolveDelay) => setTimeout(resolveDelay, 1000))
          ready = docker(['exec', name, 'pg_isready', '-q', '-U', 'sesame', '-d', 'sesame']).status === 0
        } else {
          await new Promise((resolveDelay) => setTimeout(resolveDelay, 500))
        }
      }
      assert.ok(ready, `${name} never became ready`)
    }
    assert.equal(psql(production, rehearsalRoleBootstrapSql()).status, 0)
    assert.equal(psql(production, [
      'CREATE TABLE sesame_restore_fixture (id integer);',
      'ALTER TABLE sesame_restore_fixture OWNER TO sesame_owner;',
      'GRANT SELECT ON sesame_restore_fixture TO sesame_app;',
      '',
    ].join('\n')).status, 0)
    const dump = docker(['exec', production, 'pg_dump', '-U', 'sesame_backup', 'sesame'])
    assert.equal(dump.status, 0, `pg_dump failed: ${dump.stderr}`)
    assert.match(dump.stdout, /OWNER TO sesame_owner/)
    assert.match(dump.stdout, /TO sesame_app/)

    const withoutRoles = psql(scratch, dump.stdout)
    assert.notEqual(withoutRoles.status, 0, 'the restore must fail before the roles exist')
    assert.match(withoutRoles.stderr, /role "sesame_owner" does not exist/)

    assert.equal(docker(['exec', scratch, 'psql', '-q', '-v', 'ON_ERROR_STOP=1', '-U', 'sesame', '-d', 'postgres', '-c', 'DROP DATABASE sesame', '-c', 'CREATE DATABASE sesame']).status, 0)
    assert.equal(psql(scratch, rehearsalRoleBootstrapSql()).status, 0)
    const restored = psql(scratch, dump.stdout)
    assert.equal(restored.status, 0, `the restore failed after the roles existed: ${restored.stderr}`)

    const owner = docker(['exec', scratch, 'psql', '-tAc', `SELECT tableowner FROM pg_tables WHERE tablename = 'sesame_restore_fixture'`, '-U', 'sesame', '-d', 'sesame'])
    assert.equal(owner.stdout.trim(), 'sesame_owner')
    const grants = docker(['exec', scratch, 'psql', '-tAc', `SELECT COUNT(*) FROM information_schema.table_privileges WHERE table_name = 'sesame_restore_fixture' AND grantee = 'sesame_app' AND privilege_type = 'SELECT'`, '-U', 'sesame', '-d', 'sesame'])
    assert.equal(grants.stdout.trim(), '1')
  } finally {
    docker(['rm', '-f', production, scratch])
  }
})

test('the migration entrypoint reconciles the application role', () => {
  const root = join(dirname(fileURLToPath(import.meta.url)), '..')
  const source = readFileSync(join(root, 'cmd', 'migrate', 'main.go'), 'utf8')
  assert.ok(source.includes('ReconcileRuntimeRole'), 'the migrate job does not apply the application role privileges')
})

test('the compose stack creates its own secrets before it starts', () => {
  const root = join(dirname(fileURLToPath(import.meta.url)), '..')
  const scripts = JSON.parse(readFileSync(join(root, 'package.json'), 'utf8')).scripts ?? {}
  const starters = Object.entries(scripts).filter(([, body]) => /docker\s+compose[^&|]*\bup\b/.test(body))
  assert.equal(
    starters.length,
    1,
    `expected exactly one script that starts the Compose stack, found: ${starters.map(([name]) => name).join(', ')}`,
  )
  const [name, body] = starters[0]
  assert.match(body, /deploy\/compose\/compose\.yaml/, `${name} does not start the deployment stack`)
  assert.match(scripts.setup ?? '', /scripts\/setup\.mjs/, 'the setup script no longer generates deployment secrets')
  assert.match(readFileSync(join(root, 'README.md'), 'utf8'), /npm run setup/, 'the README no longer tells a self-hoster to create the secrets first')
})
