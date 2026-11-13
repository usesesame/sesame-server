import { spawn } from 'node:child_process'
import { createHash, randomBytes } from 'node:crypto'
import { constants as fsConstants } from 'node:fs'
import {
  access,
  chmod,
  link,
  lstat,
  mkdir,
  open,
  realpath,
  stat,
  unlink,
} from 'node:fs/promises'
import { isAbsolute, join, resolve } from 'node:path'
import { Transform } from 'node:stream'
import { assertRecoveryStatus } from './selfhost-recovery.mjs'

export const MAX_DIAGNOSTIC_BYTES = 64 * 1024
export const MAX_REPORT_BYTES = 1024 * 1024
export const MAX_RECIPIENTS_SCAN_BYTES = 1024 * 1024

export class OperationError extends Error {
  constructor(code, message, options = {}) {
    super(message, options.cause ? { cause: options.cause } : undefined)
    this.name = 'OperationError'
    this.code = code
    this.historyFailed = false
    this.cleanupFailed = false
  }
}

export function nowIso() {
  return new Date().toISOString()
}

export function sameFile(left, right) {
  return Boolean(left && right && left.dev === right.dev && left.ino === right.ino)
}

export function failureCodeForFsError(error, fallback = 'publication_failed') {
  switch (error?.code) {
    case 'ENOSPC':
    case 'EDQUOT':
    case 'EFBIG':
      return 'destination_full'
    case 'EACCES':
    case 'EPERM':
    case 'EROFS':
      return 'destination_read_only'
    case 'EEXIST':
      return 'destination_exists'
    case 'EXDEV':
    case 'ENOTSUP':
    case 'EOPNOTSUPP':
    case 'ENOSYS':
      return 'destination_unsupported'
    default:
      return fallback
  }
}

export function classifyChildFailure(text) {
  const value = String(text ?? '').toLowerCase()
  if (/no space left on device|disk quota exceeded|file too large|quota exceeded/.test(value)) return 'destination_full'
  if (/read-only file system/.test(value)) return 'destination_read_only'
  if (/no identity matched|identity did not match|failed to read identity|not an age identity|incorrect identity/.test(value)) return 'identity_rejected'
  if (/failed to decrypt|failed to authenticate|invalid header|malformed|truncated|incorrect mac|authentication failed/.test(value)) return 'verification_failed'
  return null
}

export function sanitizedEnvironment(overrides = {}) {
  const environment = {}
  for (const [name, value] of Object.entries(process.env)) {
    if (name.startsWith('SESAME_')) continue
    if (typeof value === 'string') environment[name] = value
  }
  return { ...environment, ...overrides }
}

export function ageEnvironment(overrides = {}) {
  const environment = sanitizedEnvironment(overrides)
  for (const name of ['AGE_IDENTITY', 'AGE_AGENT', 'AGE_PLUGIN', 'SSH_AUTH_SOCK']) delete environment[name]
  return environment
}

export async function resolveExecutable(command, { code, label, env = process.env } = {}) {
  if (typeof command !== 'string' || command.length === 0) {
    throw new OperationError(code, `${label} is required`)
  }
  const candidates = []
  if (command.includes('/') || isAbsolute(command)) {
    candidates.push(isAbsolute(command) ? command : resolve(command))
  } else {
    for (const directory of String(env.PATH ?? '').split(':')) {
      if (directory) candidates.push(join(directory, command))
    }
  }
  for (const candidate of candidates) {
    try {
      const info = await stat(candidate)
      if (!info.isFile()) continue
      await access(candidate, fsConstants.X_OK)
      return candidate
    } catch {
      continue
    }
  }
  throw new OperationError(code, `${label} cannot be found or executed`)
}

export async function resolvePath(path, code, label) {
  if (typeof path !== 'string' || path.length === 0) {
    throw new OperationError(code, `${label} is required`)
  }
  return resolve(path)
}

export async function resolveDirectory(path, code, label) {
  const absolute = await resolvePath(path, code, label)
  let resolved
  try {
    resolved = await realpath(absolute)
  } catch (error) {
    throw new OperationError(code, `${label} is not available`, { cause: error })
  }
  const info = await stat(resolved)
  if (!info.isDirectory()) throw new OperationError(code, `${label} must be a directory`)
  return resolved
}

export async function statRegularFile(path, code, label) {
  let info
  try {
    info = await lstat(path)
  } catch (error) {
    throw new OperationError(code, `${label} cannot be read`, { cause: error })
  }
  if (!info.isFile()) throw new OperationError(code, `${label} must be a regular file`)
  return info
}

export async function openRegularFile(path, code, label) {
  const linkInfo = await statRegularFile(path, code, label)
  const handle = await open(path, 'r')
  try {
    const info = await handle.stat()
    if (!info.isFile() || !sameFile(linkInfo, info)) {
      throw new OperationError(code, `${label} changed while it was opened`)
    }
    return { handle, info }
  } catch (error) {
    await handle.close().catch(() => {})
    throw error
  }
}

export async function assertPrivateIdentityFile(path) {
  let info
  try {
    info = await lstat(path)
  } catch (error) {
    throw new OperationError('identity_rejected', 'the identity file cannot be read', { cause: error })
  }
  if (!info.isFile()) throw new OperationError('identity_rejected', 'the identity file must be a regular file')
  if ((info.mode & 0o077) !== 0) {
    throw new OperationError('identity_rejected', 'the identity file must be readable only by its owner')
  }
  return info
}

export async function assertRecipientsFile(path) {
  const info = await statRegularFile(path, 'identity_rejected', 'the recipients file')
  if (info.size > MAX_RECIPIENTS_SCAN_BYTES) {
    throw new OperationError('identity_rejected', 'the recipients file is too large')
  }
  if ((info.mode & 0o022) !== 0) {
    throw new OperationError('identity_rejected', 'the recipients file must not be writable by group or others')
  }
  const handle = await open(path, 'r')
  try {
    const scan = Buffer.alloc(MAX_RECIPIENTS_SCAN_BYTES)
    let total = 0
    while (total < scan.length) {
      const { bytesRead } = await handle.read(scan, total, scan.length - total, total)
      if (bytesRead === 0) break
      total += bytesRead
    }
    const beyond = await handle.read(Buffer.alloc(1), 0, 1, total)
    if (beyond.bytesRead !== 0) {
      throw new OperationError('identity_rejected', 'the recipients file grew beyond its size limit')
    }
    const text = scan.subarray(0, total).toString('utf8')
    if (/AGE-SECRET-KEY-/i.test(text) || /-----BEGIN [A-Z ]*PRIVATE KEY-----/.test(text)) {
      throw new OperationError('identity_rejected', 'the recipients file holds a private key; export accepts public recipients only')
    }
  } finally {
    await handle.close().catch(() => {})
  }
  return info
}

export async function syncDirectory(path) {
  const handle = await open(path, fsConstants.O_RDONLY)
  try {
    await handle.sync()
  } finally {
    await handle.close().catch(() => {})
  }
}

export class Meter extends Transform {
  constructor() {
    super()
    this.hash = createHash('sha256')
    this.bytes = 0
  }

  _transform(chunk, encoding, callback) {
    const buffer = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk)
    this.hash.update(buffer)
    this.bytes += buffer.length
    callback(null, buffer)
  }

  digest() {
    return { sha256: this.hash.copy().digest('hex'), bytes: this.bytes }
  }
}

export class BoundedCollector {
  constructor(limit, onOverflow = null) {
    this.limit = limit
    this.onOverflow = onOverflow
    this.chunks = []
    this.size = 0
    this.overflow = false
  }

  attach(stream) {
    if (!stream) return this
    stream.on('data', (chunk) => {
      const buffer = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk)
      if (this.size < this.limit) {
        const take = Math.min(buffer.length, this.limit - this.size)
        if (take > 0) {
          this.chunks.push(buffer.subarray(0, take))
          this.size += take
        }
        if (take < buffer.length) this.markOverflow()
      } else if (buffer.length > 0) {
        this.markOverflow()
      }
    })
    stream.on('error', () => {})
    return this
  }

  markOverflow() {
    if (this.overflow) return
    this.overflow = true
    if (this.onOverflow) this.onOverflow()
  }

  text() {
    return Buffer.concat(this.chunks).toString('utf8')
  }
}

export function waitForChild(child) {
  return new Promise((resolveExit) => {
    let settled = false
    const finish = (result) => {
      if (settled) return
      settled = true
      resolveExit(result)
    }
    child.once('error', (error) => finish({ code: null, signal: null, error }))
    child.once('close', (code, signal) => finish({ code, signal, error: null }))
  })
}

export function childFailed(result) {
  return Boolean(result?.error) || result?.code !== 0 || Boolean(result?.signal)
}

export class ChildSupervisor {
  constructor() {
    this.children = new Map()
    this.cancelled = false
    this.termination = null
  }

  spawn(command, args, options = {}) {
    const child = spawn(command, args, { ...options, shell: false })
    const exit = waitForChild(child)
    this.children.set(child, exit)
    exit.then(() => this.children.delete(child))
    if (this.cancelled) {
      try {
        child.kill('SIGKILL')
      } catch {}
    }
    return { child, exit }
  }

  async waitAll(timeoutMilliseconds = 0) {
    const exits = [...this.children.values()]
    if (exits.length === 0) return true
    const all = Promise.all(exits)
    if (!timeoutMilliseconds) {
      await all
      return true
    }
    let timer
    try {
      return await Promise.race([
        all.then(() => true),
        new Promise((resolveTimeout) => {
          timer = setTimeout(() => resolveTimeout(false), timeoutMilliseconds)
        }),
      ])
    } finally {
      clearTimeout(timer)
    }
  }

  async terminateAll(graceMilliseconds = 1500) {
    this.termination ??= this.terminateChildren(graceMilliseconds)
    await this.termination
    return this.children.size === 0
  }

  async cleanupResources(cleanup) {
    if (!await this.terminateAll()) {
      throw new OperationError('cleanup_failed', 'owned child processes have not stopped; temporary data was retained')
    }
    return cleanup()
  }

  async terminateChildren(graceMilliseconds) {
    this.cancelled = true
    for (const child of [...this.children.keys()]) {
      try {
        child.kill('SIGTERM')
      } catch {}
    }
    await this.waitAll(graceMilliseconds)
    for (const child of [...this.children.keys()]) {
      try {
        child.kill('SIGKILL')
      } catch {}
    }
    await this.waitAll(5000)
  }
}

export async function sha256Stream(stream) {
  const hash = createHash('sha256')
  let bytes = 0
  for await (const chunk of stream) {
    const buffer = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk)
    hash.update(buffer)
    bytes += buffer.length
  }
  return { sha256: hash.digest('hex'), bytes }
}

function stampForName(value) {
  return new Date(value).toISOString().replace(/[-:]/g, '').replace(/\.\d{3}Z$/, 'Z')
}

export async function ensurePrivateStateDirectory(path) {
  let info
  try {
    info = await lstat(path)
  } catch (error) {
    if (error.code !== 'ENOENT') {
      throw new OperationError('history_failed', 'the operation history directory cannot be read', { cause: error })
    }
    try {
      await mkdir(path, { recursive: true, mode: 0o700 })
      await chmod(path, 0o700)
    } catch (mkdirError) {
      throw new OperationError('history_failed', 'the operation history directory cannot be created', { cause: mkdirError })
    }
    info = await lstat(path)
  }
  if (!info.isDirectory() || info.isSymbolicLink()) {
    throw new OperationError('history_failed', 'the operation history directory must be a private directory')
  }
  if ((info.mode & 0o077) !== 0) {
    throw new OperationError('history_failed', 'the operation history directory must not be readable by group or others')
  }
}

export async function writeOperationRecord(stateDirectory, record) {
  await ensurePrivateStateDirectory(stateDirectory)
  const body = Buffer.from(`${JSON.stringify(record, null, 2)}\n`)
  const stamp = stampForName(record.startedAt)
  for (let attempt = 0; attempt < 10; attempt += 1) {
    const name = `${record.operation}-${stamp}-${randomBytes(6).toString('hex')}.json`
    const path = join(stateDirectory, name)
    let handle
    try {
      handle = await open(path, fsConstants.O_WRONLY | fsConstants.O_CREAT | fsConstants.O_EXCL, 0o600)
    } catch (error) {
      if (error.code === 'EEXIST') continue
      throw new OperationError('history_failed', 'the operation history could not be written', { cause: error })
    }
    try {
      await handle.writeFile(body)
      await handle.chmod(0o600)
      await handle.sync()
      await handle.close()
    } catch (error) {
      await handle.close().catch(() => {})
      await unlink(path).catch(() => {})
      throw new OperationError('history_failed', 'the operation history could not be written', { cause: error })
    }
    try {
      await syncDirectory(stateDirectory)
    } catch (error) {
      throw new OperationError('history_failed', 'the operation history could not be synced', { cause: error })
    }
    return path
  }
  throw new OperationError('history_failed', 'a unique operation history name could not be created')
}

export function parseArchiveCheckReport(text) {
  let value
  try {
    value = JSON.parse(String(text).trim())
  } catch {
    throw new OperationError('verification_failed', 'the checker did not print a JSON report')
  }
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    throw new OperationError('verification_failed', 'the checker report is not a JSON object')
  }
  const fields = ['auditHeadHash', 'auditRows', 'hasConfig', 'needsMigration', 'ownersVerified', 'schemaVersion', 'secretsVerified', 'supported']
  if (Object.keys(value).sort().join(',') !== fields.join(',')) {
    throw new OperationError('verification_failed', 'the checker report does not match the expected fields')
  }
  if (!Number.isSafeInteger(value.schemaVersion) || value.schemaVersion < 1) {
    throw new OperationError('verification_failed', 'the checker report has no valid schemaVersion')
  }
  if (!Number.isSafeInteger(value.supported) || value.supported < value.schemaVersion) {
    throw new OperationError('verification_failed', 'the checker report has no valid supported')
  }
  for (const name of ['needsMigration', 'secretsVerified', 'hasConfig']) {
    if (typeof value[name] !== 'boolean') {
      throw new OperationError('verification_failed', `the checker report has no valid ${name}`)
    }
  }
  for (const name of ['auditRows', 'ownersVerified']) {
    if (!Number.isSafeInteger(value[name]) || value[name] < 0) {
      throw new OperationError('verification_failed', `the checker report has no valid ${name}`)
    }
  }
  if (typeof value.auditHeadHash !== 'string' || !/^[0-9a-f]{64}$/.test(value.auditHeadHash)) {
    throw new OperationError('verification_failed', 'the checker report has no valid auditHeadHash')
  }
  if (value.auditRows === 0 && value.auditHeadHash !== '0'.repeat(64)) {
    throw new OperationError('verification_failed', 'the empty audit report does not have the genesis hash')
  }
  if (value.needsMigration !== (value.schemaVersion < value.supported) || value.secretsVerified !== (value.ownersVerified > 0)) {
    throw new OperationError('verification_failed', 'the checker report has inconsistent validation facts')
  }
  return value
}

export function parseRecoveryStatus(text) {
  let value
  try {
    value = JSON.parse(String(text).trim())
  } catch {
    throw new OperationError('drill_failed', 'the recovery status is not JSON')
  }
  try {
    assertRecoveryStatus(value, 'the recovery status')
  } catch {
    throw new OperationError('drill_failed', 'the recovery status is invalid')
  }
  return value
}

export class EncryptedFilePublisher {
  static async create(destination, { linkFile = link } = {}) {
    for (let attempt = 0; attempt < 10; attempt += 1) {
      const stamp = stampForName(nowIso())
      const suffix = randomBytes(6).toString('hex')
      const stagingPath = join(destination, `.sesame-${stamp}-${suffix}.tar.age.part-${process.pid}-${randomBytes(3).toString('hex')}`)
      let handle
      try {
        handle = await open(stagingPath, fsConstants.O_WRONLY | fsConstants.O_CREAT | fsConstants.O_EXCL, 0o600)
      } catch (error) {
        if (error.code === 'EEXIST') continue
        throw new OperationError(failureCodeForFsError(error), 'the ciphertext staging file could not be created', { cause: error })
      }
      try {
        await handle.chmod(0o600)
      } catch (error) {
        await handle.close().catch(() => {})
        await unlink(stagingPath).catch(() => {})
        throw new OperationError(failureCodeForFsError(error), 'the ciphertext staging file could not be made private', { cause: error })
      }
      return new EncryptedFilePublisher(destination, stagingPath, handle, linkFile)
    }
    throw new OperationError('publication_failed', 'a unique ciphertext staging file could not be created')
  }

  constructor(destination, stagingPath, handle, linkFile = link) {
    this.destination = destination
    this.stagingPath = stagingPath
    this.handle = handle
    this.linkFile = linkFile
    this.publishedPath = null
  }

  get fd() {
    return this.handle.fd
  }

  async closeWriter() {
    if (!this.handle) return
    const handle = this.handle
    this.handle = null
    try {
      await handle.sync()
      await handle.close()
    } catch (error) {
      await handle.close().catch(() => {})
      throw new OperationError(failureCodeForFsError(error), 'the ciphertext staging file could not be synced and closed', { cause: error })
    }
  }

  async readback(expected = null) {
    try {
      if (!this.handle) {
        const handle = await open(this.stagingPath, 'r')
        try {
          return await readbackHandle(handle, expected)
        } finally {
          await handle.close().catch(() => {})
        }
      }
      await this.handle.sync()
      return await readbackHandle(this.handle, expected)
    } catch (error) {
      if (error instanceof OperationError) throw error
      throw new OperationError(failureCodeForFsError(error, 'publication_failed'), 'the staged ciphertext could not be read back', { cause: error })
    }
  }

  async publish(finalPath, expected = null, verified = null) {
    const actual = verified ?? await this.readback(expected)
    if (expected && (actual.bytes !== expected.bytes || actual.sha256 !== expected.sha256)) {
      throw new OperationError('publication_failed', 'the staged ciphertext did not match its digest')
    }
    if (actual.bytes === 0) {
      throw new OperationError('encryption_failed', 'age wrote no ciphertext')
    }
    const stagingInfo = await stat(this.stagingPath)
    if (!sameFile(stagingInfo, actual)) {
      throw new OperationError('publication_failed', 'the staged ciphertext changed after readback')
    }
    try {
      await this.linkFile(this.stagingPath, finalPath)
    } catch (error) {
      if (error.code === 'EEXIST') {
        throw new OperationError('destination_exists', 'an export already has that name')
      }
      if (['EPERM', 'ENOTSUP', 'EOPNOTSUPP', 'ENOSYS', 'EXDEV'].includes(error.code)) {
        throw new OperationError('destination_unsupported', 'the destination does not support no-overwrite hard links')
      }
      throw new OperationError(failureCodeForFsError(error), 'the ciphertext could not be published', { cause: error })
    }
    this.publishedPath = finalPath
    const finalInfo = await lstat(finalPath)
    if (!finalInfo.isFile() || !sameFile(stagingInfo, finalInfo)) {
      throw new OperationError('publication_failed', 'the published ciphertext is not the staged file')
    }
    await syncDirectory(this.destination)
    await unlink(this.stagingPath).catch(() => {})
    await syncDirectory(this.destination)
    return { path: finalPath, readbackConfirmed: true }
  }

  async dispose() {
    await this.closeWriter().catch(() => {})
    await unlink(this.stagingPath).catch(() => {})
  }
}

async function readbackHandle(handle, expected) {
  const info = await handle.stat()
  if (!info.isFile()) throw new OperationError('publication_failed', 'the staged ciphertext is not a regular file')
  const stream = handle.createReadStream({ autoClose: false, start: 0 })
  const digest = await sha256Stream(stream)
  if (expected && (digest.bytes !== expected.bytes || digest.sha256 !== expected.sha256)) {
    throw new OperationError('publication_failed', 'the staged ciphertext did not match its digest')
  }
  return { ...digest, dev: info.dev, ino: info.ino }
}

export async function resolveStateDirectory(path, fallback) {
  if (path) return resolve(path)
  return join(fallback, '.sesame-export-state')
}
