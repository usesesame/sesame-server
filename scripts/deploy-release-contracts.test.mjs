import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { gzipSync } from 'node:zlib'
import { randomBytes } from 'node:crypto'
import test from 'node:test'
import {
  assertUsableBackup,
  classifyDeployment,
  compareVersions,
  deployRelease,
  parseRelease,
  readDeployedState,
  rewriteEnvImages,
  rollbackRelease,
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
    async takeBackup() { calls.backup += 1; return gzipSync(backupPayload()) },
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

test('rejects malformed manifests', () => {
  for (const broken of [
    '{"schemaVersion":2,"version":"1.0.0","commit":"' + COMMIT + '","images":{}}',
    '{"schemaVersion":1,"version":"1.0","commit":"' + COMMIT + '","images":{}}',
    '{"schemaVersion":1,"version":"1.0.0","commit":"abc","images":{}}',
    '{"schemaVersion":1,"version":"1.0.0","commit":"' + COMMIT + '","images":{"api":"registry.test.invalid/api:1.0.0","account":"registry.test.invalid/account@' + DIGEST + '","admin":"registry.test.invalid/admin@' + DIGEST + '"}}',
    '{"schemaVersion":1,"version":"1.0.0","commit":"' + COMMIT + '","images":{"api":"registry.test.invalid/api@' + DIGEST + '","account":"registry.test.invalid/account@' + DIGEST + '"}}',
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

test('accepts only a usable gzip dump as the pre-deployment backup', () => {
  const sha256 = assertUsableBackup(gzipSync(backupPayload()))
  assert.match(sha256, /^[0-9a-f]{64}$/)
  assert.throws(() => assertUsableBackup(Buffer.from('plain text')))
  assert.throws(() => assertUsableBackup(gzipSync(Buffer.from('too small'))))
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

test('refuses to redeploy the same revision, an older one, or a changed manifest', async () => {
  const release = releaseOf('1.1.0')
  const current = { version: '1.1.0', commit: COMMIT, setDigest: release.setDigest, images: release.images, deployedAt: '', previous: null }
  const same = fakeIO()
  seedEnvironment(same, { current, history: [] })
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
