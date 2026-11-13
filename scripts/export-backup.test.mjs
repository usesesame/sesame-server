import assert from 'node:assert/strict'
import { spawn, spawnSync } from 'node:child_process'
import {
  chmodSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  readdirSync,
  rmSync,
  statSync,
  utimesSync,
  writeFileSync,
} from 'node:fs'
import { createHash } from 'node:crypto'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import test from 'node:test'
import { fileURLToPath } from 'node:url'
import {
  EncryptedFilePublisher,
  ChildSupervisor,
  MAX_RECIPIENTS_SCAN_BYTES,
  assertRecipientsFile,
  ensurePrivateStateDirectory,
  parseArchiveCheckReport,
  parseRecoveryStatus,
  writeOperationRecord,
} from './encrypted-file.mjs'
import {
  buildArchiveName,
  buildDockerCreateArgs,
  buildDockerStartArgs,
  buildExportSourceSpec,
  parseArguments,
} from './export-backup.mjs'

function findOnPath(name) {
  for (const directory of String(process.env.PATH ?? '').split(':')) {
    if (!directory) continue
    const candidate = join(directory, name)
    if (existsSync(candidate)) return candidate
  }
  return null
}

const scriptsDirectory = dirname(fileURLToPath(import.meta.url))
const cli = join(scriptsDirectory, 'export-backup.mjs')
const ageBin = process.env.SESAME_AGE_BIN || findOnPath('age')
const siblingKeygen = ageBin ? join(dirname(ageBin), 'age-keygen') : null
const ageKeygenBin = process.env.SESAME_AGE_KEYGEN || (siblingKeygen && existsSync(siblingKeygen) ? siblingKeygen : findOnPath('age-keygen'))
const requireAge = process.env.SESAME_REQUIRE_AGE === '1'
const haveAge = Boolean(ageBin && ageKeygenBin && existsSync(ageBin) && existsSync(ageKeygenBin))
const report = {
  schemaVersion: 6,
  supported: 6,
  needsMigration: false,
  auditRows: 3,
  auditHeadHash: 'a'.repeat(64),
  ownersVerified: 1,
  secretsVerified: true,
  hasConfig: false,
}

const nativeSource = String.raw`#!/usr/bin/env node
const fs = require('node:fs')
const path = require('node:path')
const args = process.argv.slice(2)
const mode = process.env.FAKE_NATIVE_MODE || 'ok'
const log = process.env.FAKE_NATIVE_LOG
if (log) fs.appendFileSync(log, JSON.stringify({ args, dataDir: process.env.SESAME_DATA_DIR || '', addr: process.env.SESAME_ADDR || '', publicUrl: process.env.SESAME_PUBLIC_URL || '' }) + '\n')
const envLog = process.env.FAKE_NATIVE_ENV_LOG
if (envLog) fs.appendFileSync(envLog, JSON.stringify({ args, dataDir: process.env.SESAME_DATA_DIR || '', addr: process.env.SESAME_ADDR || '', publicUrl: process.env.SESAME_PUBLIC_URL || '' }) + '\n')
if (args[0] === 'backup' && args[1] === '--stdout') {
  if (mode === 'source-fail') { process.stdout.write('partial archive'); process.exit(1) }
  if (mode === 'zero') process.exit(0)
  if (mode === 'slow-source') { process.stdout.write('partial archive'); setTimeout(() => {}, 30000); return }
  const size = Number(process.env.FAKE_ARCHIVE_BYTES || 65536)
  const payload = Buffer.alloc(size, 0x61)
  process.stdout.write(payload)
  if (mode === 'noisy') {
    const chunk = 'x'.repeat(65536)
    for (let index = 0; index < 80; index += 1) fs.writeSync(2, chunk)
  }
  process.exit(0)
}
if (args[0] === 'check') {
  const input = fs.readFileSync(0)
  if (envLog) fs.appendFileSync(envLog, JSON.stringify({ checkBytes: input.length, dataDir: process.env.SESAME_DATA_DIR || '', addr: process.env.SESAME_ADDR || '', publicUrl: process.env.SESAME_PUBLIC_URL || '' }) + '\n')
  if (mode === 'check-fail') { process.stdout.write(JSON.stringify(${JSON.stringify(report)})); process.exit(1) }
  if (mode === 'check-malformed') { process.stdout.write('{}'); process.exit(0) }
  if (mode === 'check-huge') { process.stdout.write('x'.repeat(2 * 1024 * 1024)); process.exit(0) }
  if (mode === 'check-noisy') { const chunk = 'y'.repeat(65536); for (let index = 0; index < 80; index += 1) fs.writeSync(2, chunk) }
  process.stdout.write(JSON.stringify(${JSON.stringify(report)}))
  process.exit(0)
}
if (args[0] === 'restore') {
  const input = fs.readFileSync(0)
  if (envLog) fs.appendFileSync(envLog, JSON.stringify({ restoreBytes: input.length, dataDir: process.env.SESAME_DATA_DIR || '', addr: process.env.SESAME_ADDR || '', publicUrl: process.env.SESAME_PUBLIC_URL || '' }) + '\n')
  if (mode === 'restore-fail') { process.stdout.write('the restore failed'); process.exit(1) }
  const dataDir = process.env.SESAME_DATA_DIR
  fs.mkdirSync(dataDir, { recursive: true, mode: 0o700 })
  fs.writeFileSync(path.join(dataDir, 'fake-restored-database'), 'fictional', { mode: 0o600 })
  process.stdout.write('Restored the archive into the disposable directory.\n')
  process.exit(0)
}
if (args[0] === 'recover') {
  if (mode === 'recover-fail') process.exit(1)
  if (mode === 'recover-false') { process.stdout.write(JSON.stringify({ pending: false, generation: '', startedAt: null, completedAt: null, owners: [], devices: [] })); process.exit(0) }
  if (mode === 'recover-malformed') { process.stdout.write('not json'); process.exit(0) }
  process.stdout.write(JSON.stringify({ pending: true, generation: '0123456789abcdef0123456789abcdef', startedAt: '2026-11-07T12:00:00Z', completedAt: null, owners: [], devices: [] }))
  process.exit(0)
}
process.exit(2)
`

const ageSource = String.raw`#!/usr/bin/env node
const fs = require('node:fs')
const mode = process.env.FAKE_AGE_MODE || 'pass'
if (mode === 'ignore-term') {
  process.on('SIGTERM', () => {})
  fs.writeFileSync(process.env.FAKE_AGE_PID, String(process.pid))
  process.stdin.resume()
  setInterval(() => { if (!fs.existsSync(process.env.FAKE_AGE_PID)) process.exit(0) }, 100)
  setTimeout(() => process.exit(0), 30000)
  return
}
const log = process.env.FAKE_AGE_LOG
if (log) fs.appendFileSync(log, JSON.stringify(process.argv.slice(2)) + '\n')
if (mode === 'fail-no-read') { fs.writeSync(2, 'no identity matched any of the recipients\n'); process.exit(1) }
if (mode === 'exit-early') process.exit(0)
if (mode === 'fail-prefix') {
  const data = fs.readFileSync(0)
  fs.writeSync(1, data.subarray(0, Math.max(1, Math.floor(data.length / 2))))
  fs.writeSync(2, 'failed to decrypt and authenticate payload\n')
  process.exit(1)
}
if (mode === 'disk-full') {
  const data = fs.readFileSync(0)
  fs.writeSync(1, data.subarray(0, Math.min(1, data.length)))
  fs.writeSync(2, 'write: no space left on device\n')
  process.exit(1)
}

if (mode === 'fail') {
  fs.readFileSync(0)
  fs.writeSync(1, 'partial ciphertext')
  fs.writeSync(2, 'AGE-SECRET-KEY-1FICTIONAL\n')
  process.exit(1)
}
if (mode === 'slow') {
  const data = fs.readFileSync(0)
  fs.writeSync(1, data.subarray(0, Math.min(1, data.length)))
  setTimeout(() => {}, 30000)
  return
}
if (mode === 'slow-output') {
  const data = fs.readFileSync(0)
  fs.writeSync(1, data.subarray(0, Math.min(1, data.length)))
  setTimeout(() => {}, 30000)
  return
}
const data = fs.readFileSync(0)
fs.writeSync(1, data)
if (mode === 'noisy') {
  const chunk = 'z'.repeat(65536)
  for (let index = 0; index < 80; index += 1) fs.writeSync(2, chunk)
}
process.exit(0)
`

const dockerSource = String.raw`#!/usr/bin/env node
const fs = require('node:fs')
const args = process.argv.slice(2)
const mode = process.env.FAKE_DOCKER_MODE || 'ok'
const log = process.env.FAKE_DOCKER_LOG
if (log) fs.appendFileSync(log, JSON.stringify(args) + '\n')
const statePath = process.env.FAKE_DOCKER_STATE
function readState() { try { return JSON.parse(fs.readFileSync(statePath, 'utf8')) } catch { return {} } }
function writeState(value) { fs.writeFileSync(statePath, JSON.stringify(value)) }
if (args[0] === 'compose') { process.stdout.write('FAKE-COMPOSE-ARCHIVE'); process.exit(0) }
if (args[0] === 'volume' && args[1] === 'create') {
  if (mode === 'create-fail') process.exit(1)
  const state = readState()
  state[args.at(-1)] = { volume: true }
  writeState(state)
  if (mode === 'create-hang') { setTimeout(() => {}, 30000); return }
  if (mode === 'created-error') process.exit(1)
  process.exit(0)
}
if (args[0] === 'volume' && args[1] === 'rm') {
  if (mode.includes('cleanup-fail')) process.exit(1)
  const state = readState()
  delete state[args.at(-1)]
  writeState(state)
  process.exit(0)
}
if (args[0] === 'create') {
  if (mode === 'create-fail') process.exit(1)
  const name = args[args.indexOf('--name') + 1]
  const mount = args.indexOf('--mount')
  const state = readState()
  state[name] = { image: args[mount + 2], command: args[mount + 3], commandArgs: args.slice(mount + 4), exitCode: 0 }
  writeState(state)
  process.stdout.write(name + '-id\n')
  process.exit(0)
}
if (args[0] === 'start') {
  const name = args[args.length - 1]
  const state = readState()
  const spec = state[name] || { command: 'check', commandArgs: [] }
  if (mode === 'slow') { setTimeout(() => {}, 30000); return }
  if (spec.command === 'check') {
    fs.readFileSync(0)
    if (mode === 'check-fail' || mode === 'check-and-cleanup-fail') { state[name].exitCode = 1; writeState(state); process.exit(0) }
    process.stdout.write(JSON.stringify(${JSON.stringify(report)}))
    process.exit(0)
  }
  if (spec.command === 'restore') {
    fs.readFileSync(0)
    if (mode === 'restore-fail' || mode === 'restore-and-cleanup-fail') { state[name].exitCode = 1; writeState(state); process.exit(0) }
    process.stdout.write('Restored the archive into the disposable volume.\n')
    process.exit(0)
  }
  if (spec.command === 'recover') {
    if (mode === 'recover-fail') { state[name].exitCode = 1; writeState(state); process.exit(0) }
    if (mode === 'recover-false') { process.stdout.write(JSON.stringify({ pending: false, generation: '', startedAt: null, completedAt: null, owners: [], devices: [] })); process.exit(0) }
    process.stdout.write(JSON.stringify({ pending: true, generation: '0123456789abcdef0123456789abcdef', startedAt: '2026-11-07T12:00:00Z', completedAt: null, owners: [], devices: [] }))
    process.exit(0)
  }
  process.exit(0)
}
if (args[0] === 'inspect') {
  const name = args[args.length - 1]
  const state = readState()
  process.stdout.write(String(state[name] ? state[name].exitCode : 0))
  process.exit(0)
}
if (args[0] === 'stop') { if (mode.includes('cleanup-fail')) process.exit(1); process.exit(0) }
if (args[0] === 'rm') {
  if (mode.includes('cleanup-fail')) process.exit(1)
  const name = args[args.length - 1]
  const state = readState()
  delete state[name]
  writeState(state)
  process.exit(0)
}
process.exit(2)
`

function tempDirectory(t, prefix = 'sesame-export-test-') {
  const directory = mkdtempSync(join(tmpdir(), prefix))
  t.after(() => {
    try {
      chmodSync(directory, 0o700)
    } catch {}
    rmSync(directory, { recursive: true, force: true })
  })
  return directory
}

function writeExecutable(directory, name, body) {
  const path = join(directory, name)
  writeFileSync(path, body, { mode: 0o755 })
  chmodSync(path, 0o755)
  return path
}

function makeFakes(t) {
  const root = tempDirectory(t)
  const bin = join(root, 'bin')
  mkdirSync(bin, { mode: 0o700 })
  return {
    root,
    bin,
    native: writeExecutable(bin, 'fake-native.js', nativeSource),
    age: writeExecutable(bin, 'fake-age.js', ageSource),
    docker: writeExecutable(bin, 'docker', dockerSource),
  }
}

function writeFile(path, content, mode = 0o600) {
  writeFileSync(path, content, { mode })
  chmodSync(path, mode)
}

function runCli(args, { env = {}, timeout = 30000 } = {}) {
  return new Promise((resolveRun) => {
    const child = spawn(process.execPath, [cli, ...args], {
      env: { ...process.env, ...env },
      stdio: ['ignore', 'pipe', 'pipe'],
    })
    const stdout = []
    const stderr = []
    child.stdout.on('data', (chunk) => stdout.push(chunk))
    child.stderr.on('data', (chunk) => stderr.push(chunk))
    const timer = setTimeout(() => {
      try {
        child.kill('SIGKILL')
      } catch {}
    }, timeout)
    child.once('error', (error) => {
      clearTimeout(timer)
      resolveRun({ code: null, signal: null, error, stdout: '', stderr: '' })
    })
    child.once('close', (code, signal) => {
      clearTimeout(timer)
      resolveRun({ code, signal, error: null, stdout: Buffer.concat(stdout).toString('utf8'), stderr: Buffer.concat(stderr).toString('utf8') })
    })
  })
}

function startCli(args, { env = {} } = {}) {
  return spawn(process.execPath, [cli, ...args], {
    env: { ...process.env, ...env },
    stdio: ['ignore', 'pipe', 'pipe'],
  })
}

function waitFor(predicate, { timeout = 10000, interval = 50, label = 'condition' } = {}) {
  return new Promise((resolveWait, rejectWait) => {
    const deadline = Date.now() + timeout
    const probe = () => {
      try {
        if (predicate()) {
          resolveWait()
          return
        }
      } catch {}
      if (Date.now() >= deadline) {
        rejectWait(new Error(`${label} did not happen within ${timeout} milliseconds`))
        return
      }
      setTimeout(probe, interval)
    }
    probe()
  })
}

function parseSuccess(result, mode) {
  assert.equal(result.code, 0, `${mode} exited ${result.code}: ${result.stderr}`)
  assert.equal(result.signal, null)
  assert.notEqual(result.stdout.trim(), '')
  const record = JSON.parse(result.stdout)
  assert.deepEqual(Object.keys(record), [
    'operation',
    'startedAt',
    'completedAt',
    'success',
    'errorCode',
    'snapshot',
    'encryption',
    'publication',
    'verification',
    'drill',
  ])
  assert.equal(record.operation, mode)
  assert.equal(record.success, true)
  assert.equal(record.errorCode, null)
  assert.equal(typeof record.startedAt, 'string')
  assert.equal(typeof record.completedAt, 'string')
  return record
}

function historyRecords(stateDirectory) {
  return readdirSync(stateDirectory)
    .filter((name) => name.endsWith('.json'))
    .sort()
    .map((name) => JSON.parse(readFileSync(join(stateDirectory, name), 'utf8')))
}

function assertNoStaging(destination) {
  const names = readdirSync(destination)
  assert.deepEqual(names.filter((name) => name.includes('.part-')), [])
}

function baseExportArguments({ fakes, destination, recipients, native = null, dataDir = null, stateDir = null, age = null, extra = [] }) {
  const args = ['export']
  if (native) {
    args.push('--native', native)
    if (dataDir) args.push('--data-dir', dataDir)
  }
  args.push('--destination', destination, '--recipients-file', recipients, '--age', age ?? fakes.age)
  if (stateDir) args.push('--state-dir', stateDir)
  args.push(...extra)
  return args
}

function makeRecipientFile(directory) {
  const path = join(directory, 'recipients.txt')
  writeFile(path, 'age1fictionalrecipientfixture\n', 0o600)
  return path
}

function makeIdentityFile(directory, name = 'identity.txt') {
  const path = join(directory, name)
  writeFile(path, 'AGE-SECRET-KEY-1FICTIONALFIXTUREONLY\n', 0o600)
  return path
}

function makeArchive(directory, name = 'fixture.tar.age', content = Buffer.from('FICTIONAL-ARCHIVE-BYTES')) {
  const path = join(directory, name)
  writeFile(path, content, 0o600)
  return path
}

function sha256(buffer) {
  return createHash('sha256').update(buffer).digest('hex')
}

test('the parser accepts the documented modes and refuses a private identity for export', () => {
  const parsed = parseArguments(['export', '--native', '/bin/true', '--data-dir', '/tmp', '--destination', '/tmp', '--recipients-file', '/tmp/r'])
  assert.equal(parsed.mode, 'export')
  assert.equal(parsed.options.native, '/bin/true')
  assert.throws(
    () => parseArguments(['export', '--identity-file', '/tmp/identity', '--destination', '/tmp', '--recipients-file', '/tmp/r']),
    (error) => error.code === 'identity_rejected',
  )
  assert.throws(
    () => parseArguments(['verify', '--archive', '/tmp/a', '--identity-file', '/tmp/i', '--data-dir', '/tmp/d', '--native', '/bin/true']),
    (error) => error.code === 'usage_error',
  )
  assert.throws(
    () => parseArguments(['drill', '--archive', '/tmp/a', '--identity-file', '/tmp/i', '--native', '/bin/true', '--image', 'example/image']),
    (error) => error.code === 'usage_error',
  )
  assert.throws(
    () => parseArguments(['export', '--native', '/bin/true', '--compose-service', 'sesame', '--data-dir', '/tmp', '--destination', '/tmp', '--recipients-file', '/tmp/r']),
    (error) => error.code === 'usage_error',
  )
})

test('the Docker adapters use the agreed argv and respect the image entrypoint', () => {
  const native = buildExportSourceSpec({ kind: 'native', command: '/usr/local/bin/sesame-server', dataDir: '/var/lib/sesame' })
  assert.deepEqual(native.args, ['backup', '--stdout'])
  assert.equal(native.env.SESAME_DATA_DIR, '/var/lib/sesame')

  const live = buildExportSourceSpec({ kind: 'compose', service: 'sesame', composeFile: null, stopped: false }, '/usr/bin/docker')
  assert.equal(live.command, '/usr/bin/docker')
  assert.deepEqual(live.args, ['compose', 'exec', '-T', 'sesame', '/sesame-server', 'backup', '--stdout'])

  const stopped = buildExportSourceSpec({ kind: 'compose', service: 'sesame', composeFile: '/srv/compose.yaml', stopped: true }, '/usr/bin/docker')
  assert.deepEqual(stopped.args, ['compose', '--file', '/srv/compose.yaml', 'run', '--rm', '--no-deps', '-T', 'sesame', 'backup', '--stdout'])

  const create = buildDockerCreateArgs({
    name: 'sesame-backup-verify-fixture',
    volume: 'sesame-backup-data-fixture',
    image: 'example.invalid/sesame-server:fixture',
    command: 'check',
    args: ['--stdin', '--json'],
  })
  assert.equal(create[0], 'create')
  assert.equal(create[create.indexOf('--name') + 1], 'sesame-backup-verify-fixture')
  assert.equal(create[create.indexOf('--label') + 1], 'sesame.backup.owner=sesame-backup-verify-fixture')
  assert.ok(create.includes('--mount'))
  assert.ok(create.includes('type=volume,source=sesame-backup-data-fixture,target=/data'))
  assert.equal(create.at(-4), 'example.invalid/sesame-server:fixture')
  assert.equal(create.at(-3), 'check')
  assert.deepEqual(create.slice(-2), ['--stdin', '--json'])

  assert.deepEqual(buildDockerStartArgs('fixture', true), ['start', '--attach', '--interactive', 'fixture'])
  assert.deepEqual(buildDockerStartArgs('fixture', false), ['start', '--attach', 'fixture'])
  assert.match(buildArchiveName(new Date('2026-11-07T03:00:00Z'), 'a1b2c3'), /^sesame-20261107T030000Z-a1b2c3\.tar\.age$/)
})

test('the publisher stages a private file, confirms readback, and never overwrites a published export', async (t) => {
  const root = tempDirectory(t)
  const destination = join(root, 'destination')
  mkdirSync(destination, { mode: 0o700 })

  const publisher = await EncryptedFilePublisher.create(destination)
  writeFileSync(publisher.fd, Buffer.from('first ciphertext fixture'))
  assert.equal(statSync(publisher.stagingPath).mode & 0o777, 0o600)
  await publisher.closeWriter()
  const readback = await publisher.readback()
  assert.equal(readback.sha256, sha256(Buffer.from('first ciphertext fixture')))
  const firstPath = join(destination, 'sesame-20261107T030000Z-aaaaaa.tar.age')
  const published = await publisher.publish(firstPath, readback)
  assert.equal(published.path, firstPath)
  assert.equal(readFileSync(firstPath, 'utf8'), 'first ciphertext fixture')
  assertNoStaging(destination)
  await publisher.dispose()

  const second = await EncryptedFilePublisher.create(destination)
  writeFileSync(second.fd, Buffer.from('second ciphertext fixture'))
  await second.closeWriter()
  await assert.rejects(
    () => second.publish(firstPath),
    (error) => error.code === 'destination_exists',
  )
  assert.equal(readFileSync(firstPath, 'utf8'), 'first ciphertext fixture')
  await second.dispose()
  assertNoStaging(destination)

  const third = await EncryptedFilePublisher.create(destination)
  writeFileSync(third.fd, Buffer.from('third ciphertext fixture'))
  await third.closeWriter()
  await assert.rejects(
    () => third.publish(join(destination, 'other.tar.age'), { bytes: 1, sha256: '0'.repeat(64) }),
    (error) => error.code === 'publication_failed',
  )
  assert.equal(existsSync(join(destination, 'other.tar.age')), false)
  await third.dispose()

  const unsupported = await EncryptedFilePublisher.create(destination, {
    linkFile: async () => {
      const error = new Error('hard links are unsupported')
      error.code = 'EPERM'
      throw error
    },
  })
  writeFileSync(unsupported.fd, Buffer.from('unsupported ciphertext fixture'))
  await unsupported.closeWriter()
  const unsupportedPath = join(destination, 'unsupported.tar.age')
  await assert.rejects(
    () => unsupported.publish(unsupportedPath),
    (error) => error.code === 'destination_unsupported',
  )
  assert.equal(existsSync(unsupportedPath), false)
  await unsupported.dispose()
  assertNoStaging(destination)
})

test('operation history is private, exclusive, and reports a blocked state path', async (t) => {
  const root = tempDirectory(t)
  const state = join(root, 'state')
  const record = {
    operation: 'export',
    startedAt: '2026-11-07T03:00:00.000Z',
    completedAt: '2026-11-07T03:00:01.000Z',
    success: false,
    errorCode: 'snapshot_failed',
    snapshot: null,
    encryption: null,
    publication: null,
    verification: null,
    drill: null,
  }
  await writeOperationRecord(state, record)
  await writeOperationRecord(state, record)
  const names = readdirSync(state).sort()
  assert.equal(names.length, 2)
  assert.notEqual(names[0], names[1])
  assert.equal(statSync(state).mode & 0o777, 0o700)
  for (const name of names) assert.equal(statSync(join(state, name)).mode & 0o777, 0o600)
  assert.deepEqual(historyRecords(state)[0], record)

  const blocked = join(root, 'blocked')
  writeFile(blocked, 'not a directory', 0o600)
  await assert.rejects(
    () => ensurePrivateStateDirectory(blocked),
    (error) => error.code === 'history_failed',
  )

  const openState = join(root, 'open-state')
  mkdirSync(openState, { mode: 0o755 })
  chmodSync(openState, 0o755)
  await assert.rejects(
    () => ensurePrivateStateDirectory(openState),
    (error) => error.code === 'history_failed',
  )
})

test('the archive checker report parser requires every agreed field', () => {
  assert.deepEqual(parseArchiveCheckReport(JSON.stringify(report)), report)
  for (const broken of [
    {},
    { ...report, schemaVersion: '6' },
    { ...report, supported: 'yes' },
    { ...report, auditRows: -1 },
    { ...report, auditHeadHash: null },
  ]) {
    assert.throws(
      () => parseArchiveCheckReport(JSON.stringify(broken)),
      (error) => error.code === 'verification_failed',
    )
  }
  assert.throws(() => parseArchiveCheckReport('not json'), (error) => error.code === 'verification_failed')
})

test('checker reports reject extra fields and inconsistent validation facts', () => {
  for (const broken of [
    { ...report, privateKey: 'fictional private material' },
    { ...report, schemaVersion: 0 },
    { ...report, supported: 5 },
    { ...report, needsMigration: true },
    { ...report, auditHeadHash: 'not-a-digest' },
    { ...report, secretsVerified: false },
  ]) {
    assert.throws(() => parseArchiveCheckReport(JSON.stringify(broken)), (error) => error.code === 'verification_failed')
  }
})

test('empty audit reports require the real checker genesis', () => {
  const empty = { ...report, auditRows: 0, auditHeadHash: '0'.repeat(64), ownersVerified: 0, secretsVerified: false }
  assert.deepEqual(parseArchiveCheckReport(JSON.stringify(empty)), empty)
  for (const auditHeadHash of ['', 'a'.repeat(64)]) {
    assert.throws(() => parseArchiveCheckReport(JSON.stringify({ ...empty, auditHeadHash })), (error) => error.code === 'verification_failed')
  }
})

test('drill status rejects a pending flag without a valid recovery record', () => {
  assert.throws(() => parseRecoveryStatus(JSON.stringify({ pending: true })), (error) => error.code === 'drill_failed')
})

test('an oversized recipients file is rejected before an unscanned suffix is trusted', async (t) => {
  const path = join(tempDirectory(t), 'recipients.txt')
  writeFile(path, `${'#'.repeat(MAX_RECIPIENTS_SCAN_BYTES)}\n# AGE-SECRET-KEY-1FICTIONAL\n`, 0o600)
  await assert.rejects(() => assertRecipientsFile(path), (error) => error.code === 'identity_rejected')
})

test('ciphertext sync failure stops publication and preserves an older export', async (t) => {
  const destination = tempDirectory(t)
  const previous = join(destination, 'previous.tar.age')
  writeFile(previous, 'previous ciphertext', 0o600)
  const publisher = await EncryptedFilePublisher.create(destination)
  t.after(() => publisher.dispose())
  writeFileSync(publisher.fd, Buffer.from('new ciphertext'))
  publisher.handle.sync = async () => {
    const error = new Error('the filesystem cannot commit more bytes')
    error.code = 'ENOSPC'
    throw error
  }
  await assert.rejects(() => publisher.closeWriter(), (error) => error.code === 'destination_full')
  assert.equal(publisher.publishedPath, null)
  assert.equal(readFileSync(previous, 'utf8'), 'previous ciphertext')
})

test('a failed source stops an age child that ignores its first termination signal', async (t) => {
  const fakes = makeFakes(t)
  const destination = join(fakes.root, 'destination')
  const dataDir = join(fakes.root, 'data')
  const agePid = join(fakes.root, 'age.pid')
  mkdirSync(destination, { mode: 0o700 })
  mkdirSync(dataDir, { mode: 0o700 })
  const previous = join(destination, 'previous.tar.age')
  writeFile(previous, 'previous ciphertext', 0o600)
  const native = writeExecutable(fakes.bin, 'failed-source.cjs', `#!/usr/bin/env node\nconst fs = require('node:fs')\nfunction failWhenAgeStarts() {\n  if (!fs.existsSync(process.env.FAKE_AGE_PID)) return setTimeout(failWhenAgeStarts, 10)\n  process.stdout.write('incomplete archive')\n  process.exit(1)\n}\nfailWhenAgeStarts()\n`)
  const result = await runCli(baseExportArguments({ fakes, destination, recipients: makeRecipientFile(fakes.root), native, dataDir }), {
    env: { FAKE_AGE_MODE: 'ignore-term', FAKE_AGE_PID: agePid }, timeout: 5000,
  })
  assert.equal(result.signal, null, 'the operator hung until the test killed it')
  assert.notEqual(result.code, 0)
  assert.match(result.stderr, /snapshot_failed/)
  assert.equal(readFileSync(previous, 'utf8'), 'previous ciphertext')
  assertNoStaging(destination)
})

test('export failure at the source publishes nothing and records the failed operation', async (t) => {
  const fakes = makeFakes(t)
  const destination = join(fakes.root, 'destination')
  const dataDir = join(fakes.root, 'data')
  mkdirSync(destination, { mode: 0o700 })
  mkdirSync(dataDir, { mode: 0o700 })
  const recipients = makeRecipientFile(fakes.root)

  const result = await runCli(baseExportArguments({ fakes, destination, recipients, native: fakes.native, dataDir }), {
    env: { FAKE_NATIVE_MODE: 'source-fail' },
  })
  assert.notEqual(result.code, 0)
  assert.equal(result.stdout, '')
  assert.match(result.stderr, /snapshot_failed/)
  assert.deepEqual(readdirSync(destination).filter((name) => name.endsWith('.tar.age')), [])
  assertNoStaging(destination)
  const records = historyRecords(join(destination, '.sesame-export-state'))
  assert.equal(records.length, 1)
  assert.equal(records[0].operation, 'export')
  assert.equal(records[0].success, false)
  assert.equal(records[0].errorCode, 'snapshot_failed')
  assert.equal(records[0].snapshot, null)
  assert.equal(records[0].publication, null)

  const zeroDestination = join(fakes.root, 'zero-destination')
  const zeroDataDir = join(fakes.root, 'zero-data')
  mkdirSync(zeroDestination, { mode: 0o700 })
  mkdirSync(zeroDataDir, { mode: 0o700 })
  const zero = await runCli(baseExportArguments({ fakes, destination: zeroDestination, recipients, native: fakes.native, dataDir: zeroDataDir }), {
    env: { FAKE_NATIVE_MODE: 'zero' },
  })
  assert.notEqual(zero.code, 0)
  assert.equal(zero.stdout, '')
  assert.match(zero.stderr, /snapshot_failed/)
  assert.deepEqual(readdirSync(zeroDestination).filter((name) => name.endsWith('.tar.age')), [])
  assertNoStaging(zeroDestination)
  const zeroRecords = historyRecords(join(zeroDestination, '.sesame-export-state'))
  assert.equal(zeroRecords[0].errorCode, 'snapshot_failed')
  assert.equal(zeroRecords[0].snapshot, null)

  const brokenDestination = join(fakes.root, 'broken-destination')
  mkdirSync(brokenDestination, { mode: 0o700 })
  const broken = await runCli(baseExportArguments({ fakes, destination: brokenDestination, recipients, native: fakes.native, dataDir }), {
    env: { FAKE_AGE_MODE: 'exit-early' },
  })
  assert.notEqual(broken.code, 0)
  assert.equal(broken.stdout, '')
  assert.match(broken.stderr, /snapshot_failed|encryption_failed/)
  assert.deepEqual(readdirSync(brokenDestination).filter((name) => name.endsWith('.tar.age')), [])
  assertNoStaging(brokenDestination)
})

test('export failure at age publishes nothing and keeps the successful snapshot facts', async (t) => {
  const fakes = makeFakes(t)
  const destination = join(fakes.root, 'destination')
  const dataDir = join(fakes.root, 'data')
  mkdirSync(destination, { mode: 0o700 })
  mkdirSync(dataDir, { mode: 0o700 })
  const recipients = makeRecipientFile(fakes.root)

  const failed = await runCli(baseExportArguments({ fakes, destination, recipients, native: fakes.native, dataDir }), {
    env: { FAKE_AGE_MODE: 'fail' },
  })
  assert.notEqual(failed.code, 0)
  assert.equal(failed.stdout, '')
  assert.match(failed.stderr, /encryption_failed/)
  assert.doesNotMatch(failed.stderr, /AGE-SECRET-KEY/)
  assert.deepEqual(readdirSync(destination).filter((name) => name.endsWith('.tar.age')), [])
  assertNoStaging(destination)
  const records = historyRecords(join(destination, '.sesame-export-state'))
  assert.equal(records[0].errorCode, 'encryption_failed')
  assert.equal(records[0].snapshot.bytes, 65536)
  assert.equal(records[0].encryption, null)
  assert.equal(records[0].publication, null)

  const fullDestination = join(fakes.root, 'full-destination')
  mkdirSync(fullDestination, { mode: 0o700 })
  const full = await runCli(baseExportArguments({ fakes, destination: fullDestination, recipients, native: fakes.native, dataDir }), {
    env: { FAKE_AGE_MODE: 'disk-full' },
  })
  assert.notEqual(full.code, 0)
  assert.equal(full.stdout, '')
  assert.match(full.stderr, /destination_full/)
  assertNoStaging(fullDestination)

  const signalDestination = join(fakes.root, 'signal-destination')
  mkdirSync(signalDestination, { mode: 0o700 })
  const sigxfszAge = writeExecutable(fakes.bin, 'sigxfsz-age.sh', '#!/bin/sh\nulimit -c 0\ncat >/dev/null\nkill -s XFSZ $$\n')
  const signal = await runCli(baseExportArguments({ fakes, destination: signalDestination, recipients, native: fakes.native, dataDir, age: sigxfszAge }))
  assert.notEqual(signal.code, 0)
  assert.equal(signal.stdout, '')
  assert.match(signal.stderr, /destination_full/)
  assertNoStaging(signalDestination)
})

test('export succeeds with no identity, bounds noisy stderr, and leaves stale part files alone', async (t) => {
  const fakes = makeFakes(t)
  const destination = join(fakes.root, 'destination')
  const dataDir = join(fakes.root, 'data')
  mkdirSync(destination, { mode: 0o700 })
  mkdirSync(dataDir, { mode: 0o700 })
  const recipients = makeRecipientFile(fakes.root)
  const stale = join(destination, '.sesame-20200101T000000Z-aaaaaa.tar.age.part-1-aaaaaa')
  writeFile(stale, 'stale ciphertext fixture', 0o600)
  utimesSync(stale, new Date('2020-01-01T00:00:00Z'), new Date('2020-01-01T00:00:00Z'))

  const result = await runCli(baseExportArguments({ fakes, destination, recipients, native: fakes.native, dataDir }), {
    env: { FAKE_NATIVE_MODE: 'noisy', FAKE_AGE_MODE: 'noisy' },
  })
  const record = parseSuccess(result, 'export')
  assert.equal(record.snapshot.bytes, 65536)
  assert.equal(record.snapshot.sha256, sha256(Buffer.alloc(65536, 0x61)))
  assert.equal(record.encryption.bytes, 65536)
  assert.equal(record.encryption.sha256, record.snapshot.sha256)
  assert.equal(record.publication.readbackConfirmed, true)
  assert.equal(record.publication.sameFilesystemAsSource, true)
  assert.equal(record.verification, null)
  assert.equal(record.drill, null)
  assert.ok(record.publication.path.startsWith(destination))
  assert.equal(statSync(record.publication.path).mode & 0o777, 0o600)
  assert.equal(readFileSync(record.publication.path).length, 65536)
  assert.equal(existsSync(stale), true)
  assert.deepEqual(readdirSync(destination).filter((name) => name.includes('.part-')), [stale.split('/').at(-1)])
  assert.equal(result.stderr, '')
  const records = historyRecords(join(destination, '.sesame-export-state'))
  assert.deepEqual(records[0], record)

  const identityFlag = await runCli([
    'export',
    '--native', fakes.native,
    '--data-dir', dataDir,
    '--destination', destination,
    '--recipients-file', recipients,
    '--identity-file', join(fakes.root, 'identity.txt'),
    '--age', fakes.age,
  ])
  assert.notEqual(identityFlag.code, 0)
  assert.equal(identityFlag.stdout, '')
  assert.match(identityFlag.stderr, /identity_rejected/)
})

test('export refuses a recipients file that holds a private key and never starts the source', async (t) => {
  const fakes = makeFakes(t)
  const destination = join(fakes.root, 'destination')
  const dataDir = join(fakes.root, 'data')
  mkdirSync(destination, { mode: 0o700 })
  mkdirSync(dataDir, { mode: 0o700 })
  const recipients = join(fakes.root, 'recipients.txt')
  writeFile(recipients, 'AGE-SECRET-KEY-1FICTIONALRECIPIENT\n', 0o600)
  const marker = join(fakes.root, 'native-started.txt')

  const result = await runCli(baseExportArguments({ fakes, destination, recipients, native: fakes.native, dataDir }), {
    env: { FAKE_NATIVE_LOG: marker },
  })
  assert.notEqual(result.code, 0)
  assert.equal(result.stdout, '')
  assert.match(result.stderr, /identity_rejected/)
  assert.equal(existsSync(marker), false)
  assert.deepEqual(readdirSync(destination), [])

  const writable = join(fakes.root, 'writable.txt')
  writeFile(writable, 'age1fictionalrecipientfixture\n', 0o620)
  const writableResult = await runCli(baseExportArguments({ fakes, destination, recipients: writable, native: fakes.native, dataDir }))
  assert.notEqual(writableResult.code, 0)
  assert.match(writableResult.stderr, /identity_rejected/)
})

test('a missing age executable fails before the source starts', async (t) => {
  const fakes = makeFakes(t)
  const destination = join(fakes.root, 'destination')
  const dataDir = join(fakes.root, 'data')
  mkdirSync(destination, { mode: 0o700 })
  mkdirSync(dataDir, { mode: 0o700 })
  const recipients = makeRecipientFile(fakes.root)
  const marker = join(fakes.root, 'native-started.txt')
  const result = await runCli([
    'export',
    '--native', fakes.native,
    '--data-dir', dataDir,
    '--destination', destination,
    '--recipients-file', recipients,
    '--age', join(fakes.root, 'missing-age'),
  ], { env: { FAKE_NATIVE_LOG: marker } })
  assert.notEqual(result.code, 0)
  assert.equal(result.stdout, '')
  assert.match(result.stderr, /age_missing/)
  assert.equal(existsSync(marker), false)
  assert.deepEqual(readdirSync(destination), [])
})

test('a read-only destination leaves earlier exports unchanged', async (t) => {
  const fakes = makeFakes(t)
  const destination = join(fakes.root, 'destination')
  const dataDir = join(fakes.root, 'data')
  mkdirSync(destination, { mode: 0o700 })
  mkdirSync(dataDir, { mode: 0o700 })
  const recipients = makeRecipientFile(fakes.root)
  const previous = join(destination, 'sesame-20200101T000000Z-aaaaaa.tar.age')
  writeFile(previous, 'previous export fixture', 0o600)
  chmodSync(destination, 0o500)
  try {
    const result = await runCli(baseExportArguments({ fakes, destination, recipients, native: fakes.native, dataDir }))
    assert.notEqual(result.code, 0)
    assert.equal(result.stdout, '')
    assert.match(result.stderr, /destination_read_only/)
    assert.equal(readFileSync(previous, 'utf8'), 'previous export fixture')
    assert.deepEqual(readdirSync(destination), ['sesame-20200101T000000Z-aaaaaa.tar.age'])
  } finally {
    chmodSync(destination, 0o700)
  }
})

test('concurrent exports publish distinct complete archives', async (t) => {
  const fakes = makeFakes(t)
  const destination = join(fakes.root, 'destination')
  const dataDir = join(fakes.root, 'data')
  mkdirSync(destination, { mode: 0o700 })
  mkdirSync(dataDir, { mode: 0o700 })
  const recipients = makeRecipientFile(fakes.root)
  const args = baseExportArguments({ fakes, destination, recipients, native: fakes.native, dataDir })
  const [first, second] = await Promise.all([runCli(args), runCli(args)])
  const firstRecord = parseSuccess(first, 'export')
  const secondRecord = parseSuccess(second, 'export')
  assert.notEqual(firstRecord.publication.path, secondRecord.publication.path)
  const archives = readdirSync(destination).filter((name) => name.endsWith('.tar.age')).sort()
  assert.equal(archives.length, 2)
  assertNoStaging(destination)
  for (const name of archives) assert.equal(readFileSync(join(destination, name)).length, 65536)
  const records = historyRecords(join(destination, '.sesame-export-state'))
  assert.equal(records.length, 2)
})

test('SIGTERM cancels an export, reaps the children, and removes its staging file', async (t) => {
  const fakes = makeFakes(t)
  const destination = join(fakes.root, 'destination')
  const dataDir = join(fakes.root, 'data')
  mkdirSync(destination, { mode: 0o700 })
  mkdirSync(dataDir, { mode: 0o700 })
  const recipients = makeRecipientFile(fakes.root)
  const child = startCli(baseExportArguments({ fakes, destination, recipients, native: fakes.native, dataDir }), {
    env: { FAKE_NATIVE_MODE: 'slow-source' },
  })
  const stdout = []
  const stderr = []
  child.stdout.on('data', (chunk) => stdout.push(chunk))
  child.stderr.on('data', (chunk) => stderr.push(chunk))
  const closed = new Promise((resolveClose) => child.once('close', (code, signal) => resolveClose({ code, signal })))
  await waitFor(() => readdirSync(destination).some((name) => name.includes('.part-')), { label: 'staging file' })
  child.kill('SIGTERM')
  const result = await closed
  assert.equal(result.code, 143, Buffer.concat(stderr).toString())
  assert.equal(Buffer.concat(stdout).toString(), '')
  assert.match(Buffer.concat(stderr).toString(), /cancelled/)
  assert.deepEqual(readdirSync(destination).filter((name) => name.endsWith('.tar.age')), [])
  assertNoStaging(destination)
  assert.equal(existsSync(join(destination, '.sesame-export-state')), true)
  const records = historyRecords(join(destination, '.sesame-export-state'))
  assert.equal(records[0].errorCode, 'cancelled')
})

test('verify succeeds with both children, records the exact checker report, and removes the fresh workspace', async (t) => {
  const fakes = makeFakes(t)
  const archiveDirectory = join(fakes.root, 'archive')
  const workParent = join(fakes.root, 'work')
  mkdirSync(archiveDirectory, { mode: 0o700 })
  mkdirSync(workParent, { mode: 0o700 })
  const archive = makeArchive(archiveDirectory, 'fixture.tar.age', Buffer.alloc(4096, 0x42))
  const identity = makeIdentityFile(fakes.root)
  const envLog = join(fakes.root, 'native-env.jsonl')
  const stateDirectory = join(fakes.root, 'state')
  const before = readFileSync(archive)

  const result = await runCli([
    'verify',
    '--archive', archive,
    '--identity-file', identity,
    '--native', fakes.native,
    '--age', fakes.age,
    '--work-dir', workParent,
    '--state-dir', stateDirectory,
  ], { env: { FAKE_NATIVE_ENV_LOG: envLog } })
  const record = parseSuccess(result, 'verify')
  assert.equal(record.snapshot, null)
  assert.equal(record.encryption, null)
  assert.equal(record.publication, null)
  assert.equal(record.drill, null)
  assert.equal(record.verification.archiveSha256, sha256(before))
  assert.deepEqual(record.verification.report, report)
  assert.equal(typeof record.verification.checkedAt, 'string')
  assert.deepEqual(readFileSync(archive), before)
  assert.deepEqual(readdirSync(workParent), [])
  const entries = readFileSync(envLog, 'utf8').trim().split('\n').map((line) => JSON.parse(line))
  const check = entries.find((entry) => entry.checkBytes !== undefined)
  assert.ok(check)
  assert.equal(check.checkBytes, before.length)
  assert.ok(check.dataDir.startsWith(workParent))
  assert.equal(check.addr, '127.0.0.1:8787')
  assert.equal(existsSync(check.dataDir), false)
  const records = historyRecords(stateDirectory)
  assert.deepEqual(records[0], record)
})

test('verify bounds checker stderr and never relays it', async (t) => {
  const fakes = makeFakes(t)
  const archive = makeArchive(fakes.root)
  const identity = makeIdentityFile(fakes.root)
  const result = await runCli([
    'verify',
    '--archive', archive,
    '--identity-file', identity,
    '--native', fakes.native,
    '--age', fakes.age,
    '--state-dir', join(fakes.root, 'state'),
  ], { env: { FAKE_NATIVE_MODE: 'check-noisy' } })
  const record = parseSuccess(result, 'verify')
  assert.deepEqual(record.verification.report, report)
  assert.equal(result.stderr, '')
})

test('verify fails when the checker exits nonzero after valid JSON', async (t) => {
  const fakes = makeFakes(t)
  const archive = makeArchive(fakes.root)
  const identity = makeIdentityFile(fakes.root)
  const result = await runCli([
    'verify',
    '--archive', archive,
    '--identity-file', identity,
    '--native', fakes.native,
    '--age', fakes.age,
  ], { env: { FAKE_NATIVE_MODE: 'check-fail' } })
  assert.notEqual(result.code, 0)
  assert.equal(result.stdout, '')
  assert.match(result.stderr, /verification_failed/)
})

test('verify fails on malformed and oversized checker output', async (t) => {
  const fakes = makeFakes(t)
  const archive = makeArchive(fakes.root)
  const identity = makeIdentityFile(fakes.root)
  for (const mode of ['check-malformed', 'check-huge']) {
    const result = await runCli([
      'verify',
      '--archive', archive,
      '--identity-file', identity,
      '--native', fakes.native,
      '--age', fakes.age,
    ], { env: { FAKE_NATIVE_MODE: mode } })
    assert.notEqual(result.code, 0)
    assert.equal(result.stdout, '')
    assert.match(result.stderr, /verification_failed/)
  }
})

test('verify fails when age fails after the checker reports a valid archive', async (t) => {
  const fakes = makeFakes(t)
  const archive = makeArchive(fakes.root, 'fixture.tar.age', Buffer.alloc(4096, 0x43))
  const identity = makeIdentityFile(fakes.root)
  const prefixFailure = await runCli([
    'verify',
    '--archive', archive,
    '--identity-file', identity,
    '--native', fakes.native,
    '--age', fakes.age,
  ], { env: { FAKE_AGE_MODE: 'fail-prefix' } })
  assert.notEqual(prefixFailure.code, 0)
  assert.equal(prefixFailure.stdout, '')
  assert.match(prefixFailure.stderr, /verification_failed/)

  const identityFailure = await runCli([
    'verify',
    '--archive', archive,
    '--identity-file', identity,
    '--native', fakes.native,
    '--age', fakes.age,
  ], { env: { FAKE_AGE_MODE: 'fail-no-read' } })
  assert.notEqual(identityFailure.code, 0)
  assert.equal(identityFailure.stdout, '')
  assert.match(identityFailure.stderr, /identity_rejected/)
})

test('drill restores into a fresh workspace, proves pending recovery, and never approves or starts a service', async (t) => {
  const fakes = makeFakes(t)
  const archiveDirectory = join(fakes.root, 'archive')
  const workParent = join(fakes.root, 'work')
  mkdirSync(archiveDirectory, { mode: 0o700 })
  mkdirSync(workParent, { mode: 0o700 })
  const archive = makeArchive(archiveDirectory, 'fixture.tar.age', Buffer.alloc(4096, 0x44))
  const identity = makeIdentityFile(fakes.root)
  const envLog = join(fakes.root, 'native-env.jsonl')
  const argvLog = join(fakes.root, 'native-argv.jsonl')
  const stateDirectory = join(fakes.root, 'state')
  const before = readFileSync(archive)

  const result = await runCli([
    'drill',
    '--archive', archive,
    '--identity-file', identity,
    '--native', fakes.native,
    '--age', fakes.age,
    '--work-dir', workParent,
    '--state-dir', stateDirectory,
  ], { env: { FAKE_NATIVE_ENV_LOG: envLog, FAKE_NATIVE_LOG: argvLog } })
  const record = parseSuccess(result, 'drill')
  assert.equal(record.verification, null)
  assert.equal(record.drill.recoveryPending, true)
  assert.equal(record.drill.serviceProbed, false)
  assert.equal(typeof record.drill.restoredAt, 'string')
  assert.deepEqual(readFileSync(archive), before)
  assert.deepEqual(readdirSync(workParent), [])
  const invocations = readFileSync(argvLog, 'utf8').trim().split('\n').map((line) => JSON.parse(line))
  assert.ok(invocations.some((entry) => entry.args[0] === 'restore' && entry.args[1] === '-'))
  assert.ok(invocations.some((entry) => entry.args[0] === 'recover' && entry.args[1] === '--status' && entry.args[2] === '--json'))
  assert.equal(invocations.some((entry) => entry.args.includes('--confirm')), false)
  assert.equal(invocations.some((entry) => entry.args.includes('--retain-owner')), false)
  assert.equal(invocations.some((entry) => entry.args[0] === 'serve'), false)
  const details = readFileSync(envLog, 'utf8').trim().split('\n').map((line) => JSON.parse(line))
  assert.ok(details.some((entry) => entry.restoreBytes === before.length))
  assert.ok(details.every((entry) => entry.dataDir === '' || entry.dataDir.startsWith(workParent)))
  const records = historyRecords(stateDirectory)
  assert.deepEqual(records[0], record)
})

test('drill fails on restore failure, recovery failure, and a completed recovery', async (t) => {
  const fakes = makeFakes(t)
  const archive = makeArchive(fakes.root)
  const identity = makeIdentityFile(fakes.root)
  for (const [mode, expected] of [['restore-fail', 'drill_failed'], ['recover-fail', 'drill_failed'], ['recover-false', 'recovery_rejected'], ['recover-malformed', 'drill_failed']]) {
    const workParent = join(fakes.root, `work-${mode}`)
    mkdirSync(workParent, { mode: 0o700 })
    const result = await runCli([
      'drill',
      '--archive', archive,
      '--identity-file', identity,
      '--native', fakes.native,
      '--age', fakes.age,
      '--work-dir', workParent,
    ], { env: { FAKE_NATIVE_MODE: mode } })
    assert.notEqual(result.code, 0)
    assert.equal(result.stdout, '')
    assert.match(result.stderr, new RegExp(expected))
    assert.deepEqual(readdirSync(workParent), [])
  }
})

test('a failed history write after publication keeps the durable export and names its path', async (t) => {
  const fakes = makeFakes(t)
  const destination = join(fakes.root, 'destination')
  const dataDir = join(fakes.root, 'data')
  mkdirSync(destination, { mode: 0o700 })
  mkdirSync(dataDir, { mode: 0o700 })
  const recipients = makeRecipientFile(fakes.root)
  const statePath = join(fakes.root, 'state-file')
  writeFile(statePath, 'not a directory', 0o600)

  const result = await runCli(baseExportArguments({ fakes, destination, recipients, native: fakes.native, dataDir, stateDir: statePath }))
  assert.notEqual(result.code, 0)
  assert.equal(result.stdout, '')
  assert.match(result.stderr, /history_failed/)
  const archives = readdirSync(destination).filter((name) => name.endsWith('.tar.age'))
  assert.equal(archives.length, 1)
  const published = join(destination, archives[0])
  assert.ok(result.stderr.includes(published))
  assert.equal(readFileSync(published).length, 65536)
  assertNoStaging(destination)
})

test('Docker export uses compose exec or run, respects the entrypoint, and records a null source filesystem', async (t) => {
  const fakes = makeFakes(t)
  const recipients = makeRecipientFile(fakes.root)
  const dockerLog = join(fakes.root, 'docker.jsonl')
  const composeFile = join(fakes.root, 'compose.yaml')
  writeFile(composeFile, 'services:\n  sesame:\n    image: example.invalid/sesame:fixture\n', 0o600)
  const env = {
    PATH: `${fakes.bin}:${process.env.PATH}`,
    FAKE_DOCKER_LOG: dockerLog,
    FAKE_DOCKER_STATE: join(fakes.root, 'docker-state.json'),
  }
  const liveDestination = join(fakes.root, 'live')
  const stoppedDestination = join(fakes.root, 'stopped')
  const fileDestination = join(fakes.root, 'file')
  mkdirSync(liveDestination, { mode: 0o700 })
  mkdirSync(stoppedDestination, { mode: 0o700 })
  mkdirSync(fileDestination, { mode: 0o700 })

  const live = await runCli(['export', '--compose-service', 'sesame', '--destination', liveDestination, '--recipients-file', recipients, '--age', fakes.age], { env })
  const liveRecord = parseSuccess(live, 'export')
  assert.equal(liveRecord.publication.sameFilesystemAsSource, null)

  const stopped = await runCli(['export', '--compose-service', 'sesame', '--stopped', '--destination', stoppedDestination, '--recipients-file', recipients, '--age', fakes.age], { env })
  parseSuccess(stopped, 'export')

  const withFile = await runCli(['export', '--compose-service', 'sesame', '--compose-file', composeFile, '--destination', fileDestination, '--recipients-file', recipients, '--age', fakes.age], { env })
  parseSuccess(withFile, 'export')

  const commands = readFileSync(dockerLog, 'utf8').trim().split('\n').map((line) => JSON.parse(line))
  assert.deepEqual(commands[0], ['compose', 'exec', '-T', 'sesame', '/sesame-server', 'backup', '--stdout'])
  assert.deepEqual(commands[1], ['compose', 'run', '--rm', '--no-deps', '-T', 'sesame', 'backup', '--stdout'])
  assert.deepEqual(commands[2], ['compose', '--file', composeFile, 'exec', '-T', 'sesame', '/sesame-server', 'backup', '--stdout'])
  for (const name of ['live', 'stopped', 'file']) {
    const archives = readdirSync(join(fakes.root, name)).filter((entry) => entry.endsWith('.tar.age'))
    assert.equal(archives.length, 1)
  }
})

test('Docker verify and drill use a fresh network-none volume and clean only their own resources', async (t) => {
  const fakes = makeFakes(t)
  const archive = makeArchive(fakes.root)
  const identity = makeIdentityFile(fakes.root)
  const dockerLog = join(fakes.root, 'docker.jsonl')
  const env = {
    PATH: `${fakes.bin}:${process.env.PATH}`,
    FAKE_DOCKER_LOG: dockerLog,
    FAKE_DOCKER_STATE: join(fakes.root, 'docker-state.json'),
  }
  const verify = await runCli([
    'verify',
    '--archive', archive,
    '--identity-file', identity,
    '--image', 'example.invalid/sesame-server:fixture',
    '--age', fakes.age,
    '--state-dir', join(fakes.root, 'verify-state'),
  ], { env })
  const verifyRecord = parseSuccess(verify, 'verify')
  assert.deepEqual(verifyRecord.verification.report, report)

  const drill = await runCli([
    'drill',
    '--archive', archive,
    '--identity-file', identity,
    '--image', 'example.invalid/sesame-server:fixture',
    '--age', fakes.age,
    '--state-dir', join(fakes.root, 'drill-state'),
  ], { env })
  const drillRecord = parseSuccess(drill, 'drill')
  assert.equal(drillRecord.drill.recoveryPending, true)
  assert.equal(drillRecord.drill.serviceProbed, false)

  const commands = readFileSync(dockerLog, 'utf8').trim().split('\n').map((line) => JSON.parse(line))
  const volumeCreate = commands.find((command) => command[0] === 'volume' && command[1] === 'create')
  assert.ok(volumeCreate)
  const volumeName = volumeCreate.at(-1)
  const creates = commands.filter((command) => command[0] === 'create')
  assert.ok(creates.length >= 3)
  for (const create of creates) {
    assert.ok(create.includes('--network'))
    assert.equal(create[create.indexOf('--network') + 1], 'none')
    const mount = create[create.indexOf('--mount') + 1]
    assert.match(mount, /^type=volume,source=sesame-backup-(verify|drill)-[0-9a-f]+,target=\/data$/)
    if (create.includes('check') || create.includes('restore')) assert.ok(create.includes('--interactive'))
    if (create.includes('recover')) assert.equal(create.includes('--interactive'), false)
  }
  assert.ok(creates.some((command) => command.includes('check') && command.includes('--stdin') && command.includes('--json')))
  assert.ok(creates.some((command) => command.includes('restore') && command.includes('-')))
  assert.ok(creates.some((command) => command.includes('recover') && command.includes('--status') && command.includes('--json')))
  const stops = commands.map((command, index) => ({ command, index })).filter((entry) => entry.command[0] === 'stop')
  const removes = commands.map((command, index) => ({ command, index })).filter((entry) => entry.command[0] === 'volume' && entry.command[1] === 'rm')
  assert.ok(stops.length >= 3)
  assert.ok(removes.length >= 2)
  for (const remove of removes) {
    const earlierStop = stops.some((stop) => stop.index < remove.index)
    assert.equal(earlierStop, true)
  }
  assert.ok(commands.some((command) => command[0] === 'volume' && command[1] === 'rm' && command.at(-1) === volumeName))
})

test('Docker verify and drill fail on container failures and still clean owned resources', async (t) => {
  const fakes = makeFakes(t)
  const archive = makeArchive(fakes.root)
  const identity = makeIdentityFile(fakes.root)
  const dockerLog = join(fakes.root, 'docker.jsonl')
  const env = {
    PATH: `${fakes.bin}:${process.env.PATH}`,
    FAKE_DOCKER_LOG: dockerLog,
    FAKE_DOCKER_STATE: join(fakes.root, 'docker-state.json'),
  }
  for (const [mode, expected] of [['check-fail', 'verification_failed'], ['create-fail', 'verification_failed'], ['cleanup-fail', 'verification_failed']]) {
    const result = await runCli([
      'verify',
      '--archive', archive,
      '--identity-file', identity,
      '--image', 'example.invalid/sesame-server:fixture',
      '--age', fakes.age,
      '--state-dir', join(fakes.root, `state-${mode}`),
    ], { env: { ...env, FAKE_DOCKER_MODE: mode } })
    assert.notEqual(result.code, 0)
    assert.equal(result.stdout, '')
    assert.match(result.stderr, new RegExp(expected))
  }
  for (const [mode, expected] of [['restore-fail', 'drill_failed'], ['recover-fail', 'drill_failed'], ['recover-false', 'recovery_rejected']]) {
    const result = await runCli([
      'drill',
      '--archive', archive,
      '--identity-file', identity,
      '--image', 'example.invalid/sesame-server:fixture',
      '--age', fakes.age,
      '--state-dir', join(fakes.root, `drill-state-${mode}`),
    ], { env: { ...env, FAKE_DOCKER_MODE: mode } })
    assert.notEqual(result.code, 0)
    assert.equal(result.stdout, '')
    assert.match(result.stderr, new RegExp(expected))
  }
})

test('a lost Docker volume-create response still removes the owned volume', async (t) => {
  for (const mode of ['created-error', 'create-hang']) {
    await t.test(mode, async (t) => {
      const fakes = makeFakes(t)
      const archive = makeArchive(fakes.root)
      const identity = makeIdentityFile(fakes.root)
      const dockerLog = join(fakes.root, 'docker.jsonl')
      const statePath = join(fakes.root, 'docker-state.json')
      const args = ['verify', '--archive', archive, '--identity-file', identity,
        '--image', 'example.invalid/sesame:fixture', '--age', fakes.age,
        '--state-dir', join(fakes.root, 'state')]
      const environment = {
        PATH: `${fakes.bin}:${process.env.PATH}`, FAKE_DOCKER_LOG: dockerLog,
        FAKE_DOCKER_STATE: statePath, FAKE_DOCKER_MODE: mode,
      }
      if (mode === 'create-hang') {
        const child = startCli(args, { env: environment })
        child.stdout.resume()
        child.stderr.resume()
        t.after(() => child.kill('SIGKILL'))
        const ended = new Promise((resolveExit) => child.once('close', (code, signal) => resolveExit({ code, signal })))
        await waitFor(() => existsSync(statePath) && Object.values(JSON.parse(readFileSync(statePath, 'utf8'))).some((entry) => entry.volume), { label: 'engine-side volume creation' })
        child.kill('SIGTERM')
        const result = await ended
        assert.equal(result.code, 143)
        assert.equal(result.signal, null)
      } else {
        const result = await runCli(args, { env: environment })
        assert.notEqual(result.code, 0)
        assert.equal(result.stdout, '')
      }
      const calls = readFileSync(dockerLog, 'utf8').trim().split('\n').map((line) => JSON.parse(line))
      const created = calls.find((args) => args[0] === 'volume' && args[1] === 'create').at(-1)
      assert.ok(calls.some((args) => args[0] === 'volume' && args[1] === 'rm' && args.at(-1) === created), 'the engine-created volume was never removed')
      assert.deepEqual(JSON.parse(readFileSync(statePath, 'utf8')), {}, 'the operation left its volume in the fake engine')
    })
  }
})

test('unreaped children prevent temporary resource deletion', async () => {
  const supervisor = new ChildSupervisor()
  supervisor.children.set({ kill() {} }, new Promise(() => {}))
  assert.equal(await supervisor.terminateAll(1), false, 'unconfirmed termination was not reported')
  let removed = false
  await assert.rejects(() => supervisor.cleanupResources(() => { removed = true }), (error) => error.code === 'cleanup_failed')
  assert.equal(removed, false, 'temporary data was removed while a child might still use it')
})

test('cleanup failures remain visible after an earlier verify or restore failure', async (t) => {
  for (const [command, mode, code] of [['verify', 'check-and-cleanup-fail', 'verification_failed'], ['drill', 'restore-and-cleanup-fail', 'drill_failed']]) {
    const fakes = makeFakes(t)
    const stateDir = join(fakes.root, 'state')
    const result = await runCli([command, '--archive', makeArchive(fakes.root), '--identity-file', makeIdentityFile(fakes.root),
      '--image', 'example.invalid/sesame:fixture', '--age', fakes.age, '--state-dir', stateDir], {
      env: { PATH: `${fakes.bin}:${process.env.PATH}`, FAKE_DOCKER_MODE: mode, FAKE_DOCKER_STATE: join(fakes.root, 'engine.json') },
    })
    assert.notEqual(result.code, 0)
    assert.equal(result.stdout, '')
    assert.ok(result.stderr.includes(code), 'the cleanup warning replaced the original failure')
    assert.match(result.stderr, /cleanup failed/, 'the secondary cleanup failure was hidden')
    const record = historyRecords(stateDir)[0]
    assert.equal(record.errorCode, code)
    assert.equal(record.cleanupFailed, true)
  }
})

test('Docker cancellation explicitly stops owned containers before removing the volume', async (t) => {
  const fakes = makeFakes(t)
  const archive = makeArchive(fakes.root)
  const identity = makeIdentityFile(fakes.root)
  const dockerLog = join(fakes.root, 'docker.jsonl')
  const env = {
    PATH: `${fakes.bin}:${process.env.PATH}`,
    FAKE_DOCKER_LOG: dockerLog,
    FAKE_DOCKER_STATE: join(fakes.root, 'docker-state.json'),
    FAKE_DOCKER_MODE: 'slow',
  }
  const child = startCli([
    'verify',
    '--archive', archive,
    '--identity-file', identity,
    '--image', 'example.invalid/sesame-server:fixture',
    '--age', fakes.age,
  ], { env })
  const stdout = []
  const stderr = []
  child.stdout.on('data', (chunk) => stdout.push(chunk))
  child.stderr.on('data', (chunk) => stderr.push(chunk))
  const closed = new Promise((resolveClose) => child.once('close', (code, signal) => resolveClose({ code, signal })))
  await waitFor(() => existsSync(dockerLog) && readFileSync(dockerLog, 'utf8').includes('"start"'), { label: 'Docker start' })
  child.kill('SIGTERM')
  const result = await closed
  assert.equal(result.code, 143, Buffer.concat(stderr).toString())
  assert.equal(Buffer.concat(stdout).toString(), '')
  assert.match(Buffer.concat(stderr).toString(), /cancelled/)
  const commands = readFileSync(dockerLog, 'utf8').trim().split('\n').map((line) => JSON.parse(line))
  const stop = commands.findIndex((command) => command[0] === 'stop')
  const removeContainer = commands.findIndex((command) => command[0] === 'rm')
  const removeVolume = commands.findIndex((command) => command[0] === 'volume' && command[1] === 'rm')
  assert.ok(stop >= 0)
  assert.ok(removeContainer > stop)
  assert.ok(removeVolume > removeContainer)
})

async function generateIdentity(directory) {
  const identity = join(directory, 'real-identity.txt')
  const generated = spawnSync(ageKeygenBin, ['-o', identity], { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] })
  assert.equal(generated.status, 0, 'age-keygen failed')
  chmodSync(identity, 0o600)
  const recipient = spawnSync(ageKeygenBin, ['-y', identity], { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] })
  assert.equal(recipient.status, 0, 'age-keygen -y failed')
  const recipients = join(directory, 'real-recipients.txt')
  writeFile(recipients, `${recipient.stdout.trim()}\n`, 0o600)
  return { identity, recipients }
}

function realAgeTest(name, body) {
  if (haveAge) return test(name, body)
  if (requireAge) {
    return test(name, () => {
      assert.fail('the age and age-keygen tools are required but were not found')
    })
  }
  return test(name, { skip: 'the age tools are not available' }, () => {})
}

realAgeTest('a real age export needs no identity and decrypts to the exact archive bytes', async (t) => {
  const fakes = makeFakes(t)
  const { identity, recipients } = await generateIdentity(fakes.root)
  const destination = join(fakes.root, 'destination')
  const dataDir = join(fakes.root, 'data')
  mkdirSync(destination, { mode: 0o700 })
  mkdirSync(dataDir, { mode: 0o700 })
  const marker = join(fakes.root, 'native-started.txt')
  const result = await runCli(baseExportArguments({ fakes, destination, recipients, native: fakes.native, dataDir, age: ageBin }), {
    env: { FAKE_NATIVE_LOG: marker, FAKE_ARCHIVE_BYTES: '131072' },
  })
  const record = parseSuccess(result, 'export')
  assert.equal(record.snapshot.bytes, 131072)
  assert.ok(record.encryption.bytes > record.snapshot.bytes)
  assert.equal(statSync(record.publication.path).mode & 0o777, 0o600)
  const decrypted = spawnSync(ageBin, ['--decrypt', '--identity', identity, record.publication.path], {
    encoding: null,
    maxBuffer: 16 * 1024 * 1024,
    stdio: ['ignore', 'pipe', 'pipe'],
  })
  assert.equal(decrypted.status, 0, 'age could not decrypt the published export')
  assert.deepEqual(decrypted.stdout, Buffer.alloc(131072, 0x61))
  const records = historyRecords(join(destination, '.sesame-export-state'))
  assert.deepEqual(records[0], record)
  assert.equal(JSON.stringify(record).includes(identity), false)
  assert.equal(JSON.stringify(record).includes(recipients), false)
  assert.equal(JSON.stringify(record).includes('AGE-SECRET-KEY-'), false)
})

realAgeTest('a real age verify records the checker report and refuses a wrong identity', async (t) => {
  const fakes = makeFakes(t)
  const { identity, recipients } = await generateIdentity(fakes.root)
  const destination = join(fakes.root, 'destination')
  const dataDir = join(fakes.root, 'data')
  mkdirSync(destination, { mode: 0o700 })
  mkdirSync(dataDir, { mode: 0o700 })
  const exported = await runCli(baseExportArguments({ fakes, destination, recipients, native: fakes.native, dataDir, age: ageBin }))
  const exportRecord = parseSuccess(exported, 'export')
  const before = readFileSync(exportRecord.publication.path)

  const verify = await runCli([
    'verify',
    '--archive', exportRecord.publication.path,
    '--identity-file', identity,
    '--native', fakes.native,
    '--age', ageBin,
    '--state-dir', join(fakes.root, 'verify-state'),
  ])
  const verifyRecord = parseSuccess(verify, 'verify')
  assert.deepEqual(verifyRecord.verification.report, report)
  assert.equal(verifyRecord.verification.archiveSha256, sha256(before))

  const otherDirectory = join(fakes.root, 'other')
  mkdirSync(otherDirectory, { mode: 0o700 })
  const other = await generateIdentity(otherDirectory)
  const wrong = await runCli([
    'verify',
    '--archive', exportRecord.publication.path,
    '--identity-file', other.identity,
    '--native', fakes.native,
    '--age', ageBin,
    '--state-dir', join(fakes.root, 'wrong-state'),
  ], { env: { AGE_IDENTITY: identity, SSH_AUTH_SOCK: '/nonexistent/agent.sock' } })
  assert.notEqual(wrong.code, 0)
  assert.equal(wrong.stdout, '')
  assert.match(wrong.stderr, /identity_rejected/)
  assert.deepEqual(readFileSync(exportRecord.publication.path), before)
})

realAgeTest('a real age verify fails on a valid prefix followed by a damaged final chunk', async (t) => {
  const fakes = makeFakes(t)
  const { identity, recipients } = await generateIdentity(fakes.root)
  const destination = join(fakes.root, 'destination')
  const dataDir = join(fakes.root, 'data')
  mkdirSync(destination, { mode: 0o700 })
  mkdirSync(dataDir, { mode: 0o700 })
  const exported = await runCli(baseExportArguments({ fakes, destination, recipients, native: fakes.native, dataDir, age: ageBin }))
  const exportRecord = parseSuccess(exported, 'export')
  const damaged = join(fakes.root, 'damaged.tar.age')
  const bytes = Buffer.from(readFileSync(exportRecord.publication.path))
  bytes[bytes.length - 1] ^= 0xff
  writeFile(damaged, bytes, 0o600)
  const result = await runCli([
    'verify',
    '--archive', damaged,
    '--identity-file', identity,
    '--native', fakes.native,
    '--age', ageBin,
    '--state-dir', join(fakes.root, 'damaged-state'),
  ])
  assert.notEqual(result.code, 0)
  assert.equal(result.stdout, '')
  assert.match(result.stderr, /verification_failed/)
})

realAgeTest('a real age drill restores into a fresh workspace and leaves the source archive unchanged', async (t) => {
  const fakes = makeFakes(t)
  const { identity, recipients } = await generateIdentity(fakes.root)
  const destination = join(fakes.root, 'destination')
  const dataDir = join(fakes.root, 'data')
  const workParent = join(fakes.root, 'work')
  mkdirSync(destination, { mode: 0o700 })
  mkdirSync(dataDir, { mode: 0o700 })
  mkdirSync(workParent, { mode: 0o700 })
  const exported = await runCli(baseExportArguments({ fakes, destination, recipients, native: fakes.native, dataDir, age: ageBin }))
  const exportRecord = parseSuccess(exported, 'export')
  const before = readFileSync(exportRecord.publication.path)
  const result = await runCli([
    'drill',
    '--archive', exportRecord.publication.path,
    '--identity-file', identity,
    '--native', fakes.native,
    '--age', ageBin,
    '--work-dir', workParent,
    '--state-dir', join(fakes.root, 'drill-state'),
  ])
  const record = parseSuccess(result, 'drill')
  assert.equal(record.drill.recoveryPending, true)
  assert.equal(record.drill.serviceProbed, false)
  assert.deepEqual(readFileSync(exportRecord.publication.path), before)
  assert.deepEqual(readdirSync(workParent), [])
})
