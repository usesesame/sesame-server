import { spawn } from 'node:child_process'
import { randomBytes } from 'node:crypto'
import { chmod, mkdir, mkdtemp, rm, stat } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { pipeline } from 'node:stream/promises'
import {
  BoundedCollector,
  ChildSupervisor,
  EncryptedFilePublisher,
  MAX_DIAGNOSTIC_BYTES,
  MAX_REPORT_BYTES,
  Meter,
  OperationError,
  ageEnvironment,
  assertPrivateIdentityFile,
  assertRecipientsFile,
  childFailed,
  classifyChildFailure,
  nowIso,
  openRegularFile,
  parseArchiveCheckReport,
  parseRecoveryStatus,
  resolveDirectory,
  resolveExecutable,
  resolvePath,
  resolveStateDirectory,
  sanitizedEnvironment,
  statRegularFile,
  waitForChild,
  writeOperationRecord,
} from './encrypted-file.mjs'

const usage = `Usage: node scripts/export-backup.mjs <export|verify|drill> [options]

Export options:
  --native PATH             Use a native sesame-server executable.
  --data-dir PATH           The live data directory for --native.
  --compose-service NAME    Use a Docker Compose service.
  --compose-file PATH       Use this Docker Compose file.
  --stopped                 Use a one-off Compose run instead of exec.
  --destination PATH        The directory that receives the archive.
  --recipients-file PATH    The public age recipients file.
  --age PATH                The age executable.
  --state-dir PATH          The private operation history directory.

Verify and drill options:
  --archive PATH            The encrypted archive to read.
  --identity-file PATH      The private age identity on this device.
  --native PATH             Use a native sesame-server executable.
  --image NAME              Use a disposable Docker image.
  --age PATH                The age executable.
  --state-dir PATH          The private operation history directory.
  --work-dir PATH           The parent of the disposable workspace.
`

const optionSets = {
  export: new Set(['native', 'data-dir', 'compose-service', 'compose-file', 'stopped', 'destination', 'recipients-file', 'age', 'state-dir']),
  verify: new Set(['archive', 'identity-file', 'native', 'image', 'age', 'state-dir', 'work-dir']),
  drill: new Set(['archive', 'identity-file', 'native', 'image', 'age', 'state-dir', 'work-dir']),
}

const booleanOptions = new Set(['stopped'])

let activeSupervisor = null
let cancelledBy = null

function usageError(message) {
  return new OperationError('usage_error', message)
}

function cancelledError() {
  return new OperationError('cancelled', `the operation was cancelled by ${cancelledBy ?? 'a signal'}`)
}

function assertNotCancelled() {
  if (cancelledBy) throw cancelledError()
}

export function parseArguments(argv) {
  if (argv.length === 0) throw usageError('a mode is required')
  const mode = argv[0]
  if (!Object.hasOwn(optionSets, mode)) throw usageError('the mode must be export, verify or drill')
  const allowed = optionSets[mode]
  const options = {}
  let index = 1
  while (index < argv.length) {
    const argument = argv[index]
    if (!argument.startsWith('--') || argument === '--') throw usageError('options must start with two dashes')
    const equals = argument.indexOf('=')
    const name = equals >= 0 ? argument.slice(2, equals) : argument.slice(2)
    let value = equals >= 0 ? argument.slice(equals + 1) : undefined
    if (mode === 'export' && name === 'identity-file') {
      throw new OperationError('identity_rejected', 'export accepts a public recipients file and no identity file')
    }
    if (!allowed.has(name)) throw usageError(`the option --${name} does not apply to ${mode}`)
    if (booleanOptions.has(name)) {
      if (value !== undefined) throw usageError(`the option --${name} takes no value`)
      value = true
    } else if (value === undefined) {
      index += 1
      if (index >= argv.length) throw usageError(`the option --${name} needs a value`)
      value = argv[index]
    }
    if (Object.hasOwn(options, name)) throw usageError(`the option --${name} is used twice`)
    options[name] = value
    index += 1
  }
  if (mode === 'export') {
    const hasNative = options.native !== undefined
    const hasCompose = options['compose-service'] !== undefined
    if (hasNative === hasCompose) throw usageError('export needs exactly one of --native or --compose-service')
    if (options.destination === undefined) throw usageError('the option --destination is required')
    if (options['recipients-file'] === undefined) throw usageError('the option --recipients-file is required')
    if (hasNative && options['data-dir'] === undefined) throw usageError('--native needs --data-dir')
    if (!hasNative && options['data-dir'] !== undefined) throw usageError('--data-dir applies only to --native')
    if (hasNative && (options['compose-file'] !== undefined || options.stopped)) throw usageError('the compose options apply only to --compose-service')
  } else {
    if ((options.native !== undefined) === (options.image !== undefined)) throw usageError(`${mode} needs exactly one of --native or --image`)
    if (options.archive === undefined) throw usageError('the option --archive is required')
    if (options['identity-file'] === undefined) throw usageError('the option --identity-file is required')
  }
  return { mode, options }
}

function required(options, name) {
  const value = options[name]
  if (typeof value !== 'string' || value.length === 0) throw usageError(`the option --${name} is required`)
  return value
}

function assertSafeService(value) {
  if (!/^[A-Za-z0-9][A-Za-z0-9_.-]*$/.test(value)) throw usageError('the compose service name is not valid')
  return value
}

function assertSafeImage(value) {
  if (!/^[A-Za-z0-9][A-Za-z0-9._/:@-]*$/.test(value)) throw usageError('the image reference is not valid')
  return value
}

export function buildArchiveName(at = new Date(), suffix = randomBytes(3).toString('hex')) {
  const stamp = at.toISOString().replace(/[-:]/g, '').replace(/\.\d{3}Z$/, 'Z')
  return `sesame-${stamp}-${suffix}.tar.age`
}

export function buildExportSourceSpec(source, dockerCommand = 'docker') {
  if (source.kind === 'native') {
    return {
      command: source.command,
      args: ['backup', '--stdout'],
      env: { ...process.env, SESAME_DATA_DIR: source.dataDir },
    }
  }
  const args = ['compose']
  if (source.composeFile) args.push('--file', source.composeFile)
  if (source.stopped) {
    args.push('run', '--rm', '--no-deps', '-T', source.service, 'backup', '--stdout')
  } else {
    args.push('exec', '-T', source.service, '/sesame-server', 'backup', '--stdout')
  }
  return { command: dockerCommand, args, env: process.env }
}

export function buildDockerCreateArgs({ name, volume, image, command, args, interactive = false }) {
  return [
    'create',
    '--name', name,
    '--label', `sesame.backup.owner=${name}`,
    ...(interactive ? ['--interactive'] : []),
    '--network', 'none',
    '--mount', `type=volume,source=${volume},target=/data`,
    image,
    command,
    ...args,
  ]
}

export function buildDockerStartArgs(name, interactive = true) {
  const args = ['start', '--attach']
  if (interactive) args.push('--interactive')
  args.push(name)
  return args
}

function offlineNativeEnvironment(dataDir) {
  return sanitizedEnvironment({
    SESAME_DATA_DIR: dataDir,
    SESAME_ADDR: '127.0.0.1:8787',
    SESAME_PUBLIC_URL: 'http://localhost:8787',
  })
}

async function resolveExportOptions(options) {
  const destination = await resolveDirectory(required(options, 'destination'), 'usage_error', 'the destination')
  const recipientsFile = await resolvePath(required(options, 'recipients-file'), 'usage_error', 'the recipients file')
  await assertRecipientsFile(recipientsFile)
  const age = await resolveExecutable(options.age ?? 'age', { code: 'age_missing', label: 'age' })
  const stateDirectory = await resolveStateDirectory(options['state-dir'], destination)
  const hasNative = options.native !== undefined
  const hasCompose = options['compose-service'] !== undefined
  if (hasNative === hasCompose) throw usageError('export needs exactly one of --native or --compose-service')
  if (hasNative) {
    if (options['data-dir'] === undefined) throw usageError('--native needs --data-dir')
    if (options['compose-file'] !== undefined || options.stopped) throw usageError('the compose options apply only to --compose-service')
    const command = await resolveExecutable(options.native, { code: 'snapshot_failed', label: 'the server executable' })
    const dataDir = await resolveDirectory(options['data-dir'], 'snapshot_failed', 'the data directory')
    return { mode: 'export', destination, recipientsFile, age, stateDirectory, source: { kind: 'native', command, dataDir } }
  }
  if (options['data-dir'] !== undefined) throw usageError('--data-dir applies only to --native')
  const service = assertSafeService(required(options, 'compose-service'))
  let composeFile = null
  if (options['compose-file'] !== undefined) {
    composeFile = await resolvePath(options['compose-file'], 'usage_error', 'the compose file')
    await statRegularFile(composeFile, 'usage_error', 'the compose file')
  }
  const docker = await resolveExecutable('docker', { code: 'snapshot_failed', label: 'docker' })
  return {
    mode: 'export',
    destination,
    recipientsFile,
    age,
    stateDirectory,
    source: { kind: 'compose', service, composeFile, stopped: Boolean(options.stopped), docker },
  }
}

async function resolveVerifyOptions(mode, options) {
  const failureCode = mode === 'verify' ? 'verification_failed' : 'drill_failed'
  const archive = await resolvePath(required(options, 'archive'), 'usage_error', 'the archive')
  const identityFile = await resolvePath(required(options, 'identity-file'), 'usage_error', 'the identity file')
  await assertPrivateIdentityFile(identityFile)
  const age = await resolveExecutable(options.age ?? 'age', { code: 'age_missing', label: 'age' })
  const workParent = options['work-dir'] !== undefined
    ? await resolveDirectory(options['work-dir'], 'usage_error', 'the work directory')
    : null
  const stateDirectory = await resolveStateDirectory(options['state-dir'], dirname(archive))
  const hasNative = options.native !== undefined
  const hasImage = options.image !== undefined
  if (hasNative === hasImage) throw usageError(`${mode} needs exactly one of --native or --image`)
  if (hasNative) {
    const native = await resolveExecutable(options.native, { code: failureCode, label: 'the server executable' })
    return { mode, archive, identityFile, age, workParent, stateDirectory, native, image: null, docker: null }
  }
  const image = assertSafeImage(required(options, 'image'))
  const docker = await resolveExecutable('docker', { code: failureCode, label: 'docker' })
  return { mode, archive, identityFile, age, workParent, stateDirectory, native: null, image, docker }
}

function baseRecord(operation, startedAt) {
  return {
    operation,
    startedAt,
    completedAt: null,
    success: false,
    errorCode: null,
    snapshot: null,
    encryption: null,
    publication: null,
    verification: null,
    drill: null,
  }
}

function normalizeOperationError(error, code, message) {
  if (error instanceof OperationError) return error
  return new OperationError(code, message, { cause: error })
}

function operationFailure(error, code, message) {
  if (cancelledBy) return cancelledError()
  return normalizeOperationError(error, code, message)
}

async function recordHistory(record, stateDirectory) {
  try {
    await writeOperationRecord(stateDirectory, record)
    return null
  } catch (error) {
    return normalizeOperationError(error, 'history_failed', 'the operation history could not be written')
  }
}

async function finishFailure(record, stateDirectory, failure) {
  record.completedAt = nowIso()
  record.success = false
  record.errorCode = failure.code
  if (failure.cleanupFailed) record.cleanupFailed = true
  const historyError = await recordHistory(record, stateDirectory)
  if (historyError) failure.historyFailed = true
  throw failure
}

async function finishSuccess(record, stateDirectory) {
  record.completedAt = nowIso()
  record.success = true
  record.errorCode = null
  const historyError = await recordHistory(record, stateDirectory)
  if (historyError) {
    const publishedPath = record.publication?.path
    const message = publishedPath
      ? `the export was published at ${publishedPath} but the operation history could not be written`
      : 'the operation history could not be written'
    throw new OperationError('history_failed', message)
  }
  return record
}

async function sameFilesystemAsSource(options) {
  if (options.source.kind !== 'native') return null
  try {
    const [source, destination] = await Promise.all([stat(options.source.dataDir), stat(options.destination)])
    return source.dev === destination.dev
  } catch {
    return null
  }
}

async function runExportPipeline({ supervisor, sourceSpec, age, recipientsFile, stagingFd }) {
  const source = supervisor.spawn(sourceSpec.command, sourceSpec.args, {
    env: sourceSpec.env,
    stdio: ['ignore', 'pipe', 'pipe'],
  })
  const encrypted = supervisor.spawn(age, ['--encrypt', '--recipients-file', recipientsFile], {
    env: ageEnvironment(),
    stdio: ['pipe', stagingFd, 'pipe'],
  })
  const sourceErr = new BoundedCollector(MAX_DIAGNOSTIC_BYTES).attach(source.child.stderr)
  const ageErr = new BoundedCollector(MAX_DIAGNOSTIC_BYTES).attach(encrypted.child.stderr)
  const meter = new Meter()
  let firstFailure = null
  let sourceCompletedAt = null
  const sourceExit = source.exit.then((result) => {
    if (childFailed(result)) firstFailure ??= 'source'
    else sourceCompletedAt = nowIso()
    return result
  })
  const ageExit = encrypted.exit.then((result) => {
    if (childFailed(result)) firstFailure ??= 'age'
    return result
  })
  const stream = pipeline(source.child.stdout, meter, encrypted.child.stdin).catch((error) => {
    void supervisor.terminateAll()
    return error
  })
  sourceExit.then((result) => {
    if (childFailed(result)) {
      encrypted.child.stdin.destroy()
      void supervisor.terminateAll()
    }
  })
  ageExit.then((result) => {
    if (childFailed(result)) {
      void supervisor.terminateAll()
      source.child.stdout.destroy()
    }
  })
  const [sourceResult, ageResult, streamError] = await Promise.all([sourceExit, ageExit, stream])
  return { sourceResult, ageResult, streamError, firstFailure, sourceCompletedAt, meter, sourceErr, ageErr }
}

async function runDecryptPipeline({ supervisor, age, identityFile, archive, target }) {
  const decrypted = supervisor.spawn(age, ['--decrypt', '--identity', identityFile], {
    env: ageEnvironment(),
    stdio: ['pipe', 'pipe', 'pipe'],
  })
  const targetProcess = target.spawn(supervisor)
  const ageErr = new BoundedCollector(MAX_DIAGNOSTIC_BYTES).attach(decrypted.child.stderr)
  const targetErr = new BoundedCollector(MAX_DIAGNOSTIC_BYTES).attach(targetProcess.child.stderr)
  const targetOut = new BoundedCollector(MAX_REPORT_BYTES, () => {
    void supervisor.terminateAll()
  }).attach(targetProcess.child.stdout)
  const meter = new Meter()
  const archiveStream = archive.handle.createReadStream({ autoClose: false, start: 0 })
  let firstFailure = null
  const ageExit = decrypted.exit.then((result) => {
    if (childFailed(result)) firstFailure ??= 'age'
    return result
  })
  const targetExit = targetProcess.exit.then((result) => {
    if (childFailed(result)) firstFailure ??= 'target'
    return result
  })
  const input = pipeline(archiveStream, meter, decrypted.child.stdin).catch((error) => {
    void supervisor.terminateAll()
    return error
  })
  const delivery = pipeline(decrypted.child.stdout, targetProcess.child.stdin).catch((error) => {
    void supervisor.terminateAll()
    return error
  })
  ageExit.then((result) => {
    if (childFailed(result)) {
      targetProcess.child.stdin.destroy()
      void supervisor.terminateAll()
    }
  })
  targetExit.then((result) => {
    if (childFailed(result)) {
      void supervisor.terminateAll()
      decrypted.child.stdout.destroy()
    }
  })
  const [ageResult, targetResult, inputError, deliveryError] = await Promise.all([ageExit, targetExit, input, delivery])
  let containerExit = null
  if (target.inspectExit && !cancelledBy && !childFailed(targetResult)) {
    containerExit = await target.inspectExit()
    if (containerExit !== 0 && firstFailure === null) firstFailure = 'target'
  }
  return { ageResult, targetResult, inputError, deliveryError, firstFailure, meter, ageErr, targetErr, targetOut, containerExit }
}

function createNativeTarget(command, args, env) {
  return {
    spawn: (supervisor) => supervisor.spawn(command, args, { env, stdio: ['pipe', 'pipe', 'pipe'] }),
    inspectExit: null,
  }
}

class DockerResources {
  constructor({ supervisor, docker, image, failureCode, prefix }) {
    this.supervisor = supervisor
    this.docker = docker
    this.image = image
    this.failureCode = failureCode
    this.prefix = prefix
    this.volume = null
    this.containers = []
  }

  async createVolume() {
    const name = `${this.prefix}-${randomBytes(8).toString('hex')}`
    this.volume = name
    try {
      await this.run(['volume', 'create', '--label', `sesame.backup.owner=${name}`, name], 'the disposable volume could not be created')
    } catch (error) {
      throw normalizeOperationError(error, this.failureCode, 'the disposable volume could not be created')
    }
    return name
  }

  async createContainer(command, args, interactive = false) {
    const name = `${this.prefix}-${command}-${randomBytes(8).toString('hex')}`
    this.containers.push(name)
    try {
      await this.run(buildDockerCreateArgs({ name, volume: this.volume, image: this.image, command, args, interactive }), 'the disposable container could not be created')
    } catch (error) {
      throw normalizeOperationError(error, this.failureCode, 'the disposable container could not be created')
    }
    return name
  }

  startTarget(name) {
    return {
      spawn: (supervisor) => supervisor.spawn(this.docker, buildDockerStartArgs(name, true), {
        env: sanitizedEnvironment(),
        stdio: ['pipe', 'pipe', 'pipe'],
      }),
      inspectExit: () => this.inspectExit(name),
    }
  }

  async inspectExit(name) {
    const result = await this.run(['inspect', '--format', '{{.State.ExitCode}}', name], 'the disposable container state could not be read')
    const code = Number.parseInt(result.stdout.trim(), 10)
    if (!Number.isInteger(code)) throw new OperationError(this.failureCode, 'the disposable container state has no exit code')
    return code
  }

  async runContainer(name, interactive = false) {
    const { child, exit } = this.supervisor.spawn(this.docker, buildDockerStartArgs(name, interactive), {
      env: sanitizedEnvironment(),
      stdio: [interactive ? 'pipe' : 'ignore', 'pipe', 'pipe'],
    })
    const stdout = new BoundedCollector(MAX_REPORT_BYTES, () => {
      void this.supervisor.terminateAll()
    }).attach(child.stdout)
    new BoundedCollector(MAX_DIAGNOSTIC_BYTES).attach(child.stderr)
    const result = await exit
    if (cancelledBy) throw cancelledError()
    const exitCode = childFailed(result) ? null : await this.inspectExit(name)
    if (childFailed(result) || exitCode !== 0) {
      throw new OperationError(this.failureCode, 'the disposable container command failed')
    }
    if (stdout.overflow) throw new OperationError(this.failureCode, 'the disposable container printed too much output')
    return stdout.text()
  }

  async run(args, message) {
    const { child, exit } = this.supervisor.spawn(this.docker, args, {
      env: sanitizedEnvironment(),
      stdio: ['ignore', 'pipe', 'pipe'],
    })
    const stdout = new BoundedCollector(MAX_REPORT_BYTES).attach(child.stdout)
    const stderr = new BoundedCollector(MAX_DIAGNOSTIC_BYTES).attach(child.stderr)
    const result = await exit
    if (childFailed(result)) {
      const classified = classifyChildFailure(stderr.text())
      throw new OperationError(classified ?? this.failureCode, message)
    }
    if (stdout.overflow) throw new OperationError(this.failureCode, message)
    return { stdout: stdout.text(), stderr: stderr.text() }
  }

  async cleanup() {
    const failures = []
    for (const name of this.containers) {
      const stopped = await cleanupCommand(this.docker, ['stop', '--time', '1', name])
      if (!stopped.ok && !/no such container|is not running/i.test(stopped.stderr)) failures.push(name)
      const removed = await cleanupCommand(this.docker, ['rm', '--force', name])
      if (!removed.ok && !/no such container/i.test(removed.stderr)) failures.push(name)
    }
    if (this.volume) {
      const removed = await cleanupCommand(this.docker, ['volume', 'rm', '--force', this.volume])
      if (!removed.ok && !/no such volume/i.test(removed.stderr)) failures.push(this.volume)
    }
    if (failures.length > 0) {
      throw new OperationError(this.failureCode, 'the disposable Docker resources could not be removed')
    }
  }
}

async function cleanupCommand(command, args) {
  const child = spawn(command, args, { env: sanitizedEnvironment(), stdio: ['ignore', 'pipe', 'pipe'], shell: false })
  const stderr = new BoundedCollector(MAX_DIAGNOSTIC_BYTES).attach(child.stderr)
  new BoundedCollector(MAX_DIAGNOSTIC_BYTES).attach(child.stdout)
  let timer
  try {
    const result = await Promise.race([
      waitForChild(child),
      new Promise((resolveTimeout) => {
        timer = setTimeout(() => {
          try {
            child.kill('SIGKILL')
          } catch {}
          resolveTimeout({ code: null, signal: 'SIGKILL', error: new Error('cleanup timed out') })
        }, 2000)
      }),
    ])
    return { ok: !childFailed(result), stderr: stderr.text() }
  } finally {
    clearTimeout(timer)
  }
}

async function createWorkspace(workParent) {
  const parent = workParent ?? tmpdir()
  const workspace = await mkdtemp(join(parent, 'sesame-backup-'))
  await chmod(workspace, 0o700)
  return workspace
}

async function cleanupWorkspace(workspace, code) {
  if (!workspace) return null
  try {
    await rm(workspace, { recursive: true, force: true, maxRetries: 2 })
    return null
  } catch (error) {
    return new OperationError(code, 'the disposable workspace could not be removed', { cause: error })
  }
}

async function cleanupDocker(resources) {
  if (!resources) return null
  try {
    await resources.cleanup()
    return null
  } catch (error) {
    return normalizeOperationError(error, resources.failureCode, 'the disposable Docker resources could not be removed')
  }
}

async function runExport(options) {
  const startedAt = nowIso()
  const record = baseRecord('export', startedAt)
  const supervisor = new ChildSupervisor()
  activeSupervisor = supervisor
  let publisher = null
  let failure = null
  let sameFilesystem = null
  let stage = 'publication'
  try {
    assertNotCancelled()
    publisher = await EncryptedFilePublisher.create(options.destination)
    stage = 'snapshot'
    const sourceSpec = buildExportSourceSpec(options.source, options.source.docker)
    const run = await runExportPipeline({
      supervisor,
      sourceSpec,
      age: options.age,
      recipientsFile: options.recipientsFile,
      stagingFd: publisher.fd,
    })
    assertNotCancelled()
    if (run.meter.bytes > 0 && !childFailed(run.sourceResult)) {
      const digest = run.meter.digest()
      record.snapshot = { completedAt: run.sourceCompletedAt, bytes: digest.bytes, sha256: digest.sha256 }
    }
    if (run.firstFailure === 'source') {
      throw new OperationError('snapshot_failed', 'the source backup did not produce an archive')
    }
    if (run.firstFailure === 'age' || childFailed(run.ageResult)) {
      const classified = run.ageResult.signal === 'SIGXFSZ' ? 'destination_full' : classifyChildFailure(run.ageErr.text())
      throw new OperationError(classified && classified.startsWith('destination_') ? classified : 'encryption_failed', 'age could not encrypt the archive')
    }
    if (childFailed(run.sourceResult) || run.meter.bytes === 0) {
      throw new OperationError('snapshot_failed', 'the source backup did not produce an archive')
    }
    if (run.streamError) throw new OperationError('encryption_failed', 'the encryption stream did not finish')
    stage = 'encryption'
    await publisher.closeWriter()
    const readback = await publisher.readback()
    record.encryption = { completedAt: nowIso(), bytes: readback.bytes, sha256: readback.sha256 }
    if (readback.bytes === 0) throw new OperationError('encryption_failed', 'age wrote no ciphertext')
    stage = 'publication'
    sameFilesystem = await sameFilesystemAsSource(options)
    const finalPath = join(options.destination, buildArchiveName())
    const publication = await publisher.publish(finalPath, { bytes: readback.bytes, sha256: readback.sha256 }, readback)
    record.publication = {
      path: publication.path,
      readbackConfirmed: publication.readbackConfirmed,
      sameFilesystemAsSource: sameFilesystem,
    }
  } catch (error) {
    const fallback = stage === 'snapshot' ? 'snapshot_failed' : stage === 'encryption' ? 'encryption_failed' : 'publication_failed'
    failure = operationFailure(error, fallback, 'the export could not be completed')
    if (!record.publication && publisher?.publishedPath) {
      record.publication = {
        path: publisher.publishedPath,
        readbackConfirmed: true,
        sameFilesystemAsSource: sameFilesystem,
      }
    }
    if (failure.code === 'cancelled' && record.publication) {
      failure = new OperationError('cancelled', `the export was published at ${record.publication.path} but the operation was cancelled`)
    }
  } finally {
    try {
      await supervisor.cleanupResources(async () => {
        if (publisher) await publisher.dispose()
      })
    } catch (error) {
      failure = normalizeOperationError(error, 'cleanup_failed', 'the export resources could not be cleaned up')
    }
    activeSupervisor = null
    if (!failure && cancelledBy) {
      failure = record.publication
        ? new OperationError('cancelled', `the export was published at ${record.publication.path} but the operation was cancelled`)
        : cancelledError()
    }
  }
  if (failure) return finishFailure(record, options.stateDirectory, failure)
  return finishSuccess(record, options.stateDirectory)
}

async function runVerify(options) {
  const startedAt = nowIso()
  const record = baseRecord('verify', startedAt)
  const supervisor = new ChildSupervisor()
  activeSupervisor = supervisor
  let archive = null
  let workspace = null
  let docker = null
  let failure = null
  try {
    assertNotCancelled()
    archive = await openRegularFile(options.archive, 'verification_failed', 'the archive')
    if (archive.info.size === 0) throw new OperationError('verification_failed', 'the archive is empty')
    let target
    if (options.native) {
      workspace = await createWorkspace(options.workParent)
      const dataDir = join(workspace, 'data')
      await mkdir(dataDir, { mode: 0o700 })
      await chmod(dataDir, 0o700)
      target = createNativeTarget(options.native, ['check', '--stdin', '--json'], offlineNativeEnvironment(dataDir))
    } else {
      docker = new DockerResources({
        supervisor,
        docker: options.docker,
        image: options.image,
        failureCode: 'verification_failed',
        prefix: 'sesame-backup-verify',
      })
      await docker.createVolume()
      const name = await docker.createContainer('check', ['--stdin', '--json'], true)
      target = docker.startTarget(name)
    }
    assertNotCancelled()
    const run = await runDecryptPipeline({
      supervisor,
      age: options.age,
      identityFile: options.identityFile,
      archive,
      target,
    })
    assertNotCancelled()
    if (run.firstFailure === 'target') {
      throw new OperationError('verification_failed', 'the checker failed')
    }
    if (run.firstFailure === 'age' || childFailed(run.ageResult)) {
      const classified = classifyChildFailure(run.ageErr.text())
      throw new OperationError(classified ?? 'verification_failed', 'the archive could not be decrypted')
    }
    if (childFailed(run.targetResult) || (run.containerExit !== null && run.containerExit !== 0)) {
      throw new OperationError('verification_failed', 'the checker failed')
    }
    if (run.targetOut.overflow) throw new OperationError('verification_failed', 'the checker printed too much output')
    if (run.inputError || run.deliveryError) throw new OperationError('verification_failed', 'the verification stream did not finish')
    if (run.meter.bytes !== archive.info.size) throw new OperationError('verification_failed', 'the archive stream did not finish')
    const report = parseArchiveCheckReport(run.targetOut.text())
    record.verification = {
      archiveSha256: run.meter.digest().sha256,
      checkedAt: nowIso(),
      report,
    }
  } catch (error) {
    failure = operationFailure(error, 'verification_failed', 'the verification could not be completed')
  } finally {
    let cleanupError = null
    let workspaceError = null
    try {
      await supervisor.cleanupResources(async () => {
        if (archive) await archive.handle.close().catch(() => {})
        cleanupError = await cleanupDocker(docker)
        workspaceError = await cleanupWorkspace(workspace, 'verification_failed')
      })
    } catch (error) {
      failure = normalizeOperationError(error, 'cleanup_failed', 'the verification resources could not be cleaned up')
    }
    activeSupervisor = null
    if (!failure) failure = cancelledBy ? cancelledError() : cleanupError ?? workspaceError
    if (failure && (cleanupError || workspaceError)) failure.cleanupFailed = true
  }
  if (failure) return finishFailure(record, options.stateDirectory, failure)
  return finishSuccess(record, options.stateDirectory)
}

async function runDrill(options) {
  const startedAt = nowIso()
  const record = baseRecord('drill', startedAt)
  const supervisor = new ChildSupervisor()
  activeSupervisor = supervisor
  let archive = null
  let workspace = null
  let docker = null
  let failure = null
  try {
    assertNotCancelled()
    archive = await openRegularFile(options.archive, 'drill_failed', 'the archive')
    if (archive.info.size === 0) throw new OperationError('drill_failed', 'the archive is empty')
    let restoredAt = null
    let status
    if (options.native) {
      workspace = await createWorkspace(options.workParent)
      const dataDir = join(workspace, 'data')
      await mkdir(dataDir, { mode: 0o700 })
      await chmod(dataDir, 0o700)
      const target = createNativeTarget(options.native, ['restore', '-'], offlineNativeEnvironment(dataDir))
      assertNotCancelled()
      const run = await runDecryptPipeline({
        supervisor,
        age: options.age,
        identityFile: options.identityFile,
        archive,
        target,
      })
      assertNotCancelled()
      if (run.firstFailure === 'target') throw new OperationError('drill_failed', 'the restore failed')
      if (run.firstFailure === 'age' || childFailed(run.ageResult)) {
        const classified = classifyChildFailure(run.ageErr.text())
        throw new OperationError(classified ?? 'drill_failed', 'the archive could not be decrypted')
      }
      if (childFailed(run.targetResult)) throw new OperationError('drill_failed', 'the restore failed')
      if (run.targetOut.overflow) throw new OperationError('drill_failed', 'the restore printed too much output')
      if (run.inputError || run.deliveryError) throw new OperationError('drill_failed', 'the restore stream did not finish')
      if (run.meter.bytes !== archive.info.size) throw new OperationError('drill_failed', 'the archive stream did not finish')
      restoredAt = nowIso()
      const statusResult = await runSimpleCommand(supervisor, options.native, ['recover', '--status', '--json'], {
        env: offlineNativeEnvironment(dataDir),
        code: 'drill_failed',
        label: 'the recovery status command',
      })
      status = parseRecoveryStatus(statusResult.stdout)
    } else {
      docker = new DockerResources({
        supervisor,
        docker: options.docker,
        image: options.image,
        failureCode: 'drill_failed',
        prefix: 'sesame-backup-drill',
      })
      await docker.createVolume()
      const restoreName = await docker.createContainer('restore', ['-'], true)
      assertNotCancelled()
      const run = await runDecryptPipeline({
        supervisor,
        age: options.age,
        identityFile: options.identityFile,
        archive,
        target: docker.startTarget(restoreName),
      })
      assertNotCancelled()
      if (run.firstFailure === 'target') throw new OperationError('drill_failed', 'the restore failed')
      if (run.firstFailure === 'age' || childFailed(run.ageResult)) {
        const classified = classifyChildFailure(run.ageErr.text())
        throw new OperationError(classified ?? 'drill_failed', 'the archive could not be decrypted')
      }
      if (childFailed(run.targetResult) || (run.containerExit !== null && run.containerExit !== 0)) {
        throw new OperationError('drill_failed', 'the restore failed')
      }
      if (run.targetOut.overflow) throw new OperationError('drill_failed', 'the restore printed too much output')
      if (run.inputError || run.deliveryError) throw new OperationError('drill_failed', 'the restore stream did not finish')
      if (run.meter.bytes !== archive.info.size) throw new OperationError('drill_failed', 'the archive stream did not finish')
      restoredAt = nowIso()
      const recoverName = await docker.createContainer('recover', ['--status', '--json'])
      const output = await docker.runContainer(recoverName, false)
      status = parseRecoveryStatus(output)
    }
    if (!status.pending) {
      throw new OperationError('recovery_rejected', 'the restored database has no pending recovery')
    }
    record.drill = { restoredAt, recoveryPending: true, serviceProbed: false }
    assertNotCancelled()
  } catch (error) {
    failure = operationFailure(error, 'drill_failed', 'the drill could not be completed')
  } finally {
    let cleanupError = null
    let workspaceError = null
    try {
      await supervisor.cleanupResources(async () => {
        if (archive) await archive.handle.close().catch(() => {})
        cleanupError = await cleanupDocker(docker)
        workspaceError = await cleanupWorkspace(workspace, 'drill_failed')
      })
    } catch (error) {
      failure = normalizeOperationError(error, 'cleanup_failed', 'the drill resources could not be cleaned up')
    }
    activeSupervisor = null
    if (!failure) failure = cancelledBy ? cancelledError() : cleanupError ?? workspaceError
    if (failure && (cleanupError || workspaceError)) failure.cleanupFailed = true
  }
  if (failure) return finishFailure(record, options.stateDirectory, failure)
  return finishSuccess(record, options.stateDirectory)
}

async function runSimpleCommand(supervisor, command, args, { env, code, label, stdoutLimit = MAX_REPORT_BYTES }) {
  const { child, exit } = supervisor.spawn(command, args, {
    env,
    stdio: ['ignore', 'pipe', 'pipe'],
  })
  const stdout = new BoundedCollector(stdoutLimit, () => {
    void supervisor.terminateAll()
  }).attach(child.stdout)
  const stderr = new BoundedCollector(MAX_DIAGNOSTIC_BYTES).attach(child.stderr)
  const result = await exit
  if (childFailed(result)) {
    const classified = classifyChildFailure(stderr.text())
    throw new OperationError(classified ?? code, `${label} failed`)
  }
  if (stdout.overflow) throw new OperationError(code, `${label} printed too much output`)
  return { stdout: stdout.text(), stderr: stderr.text() }
}

function printRecord(record) {
  const text = `${JSON.stringify(record, null, 2)}\n`
  return new Promise((resolveWrite, rejectWrite) => {
    process.stdout.write(text, (error) => {
      if (error) rejectWrite(error)
      else resolveWrite()
    })
  })
}

function installSignalHandlers() {
  const handle = (signal, code) => {
    if (cancelledBy) process.exit(code)
    cancelledBy = signal
    if (activeSupervisor) activeSupervisor.terminateAll()
    setTimeout(() => process.exit(code), 30000).unref()
  }
  process.once('SIGINT', () => handle('SIGINT', 130))
  process.once('SIGTERM', () => handle('SIGTERM', 143))
}

function reportFailure(mode, error) {
  const normalized = normalizeOperationError(error, 'operation_failed', 'the operation could not be completed')
  const label = ['export', 'verify', 'drill'].includes(mode) ? mode : 'export-backup'
  const warnings = []
  if (normalized.cleanupFailed) warnings.push('temporary data may remain because cleanup failed')
  if (normalized.historyFailed) warnings.push('the operation history was not written')
  const suffix = warnings.length ? `; ${warnings.join('; ')}` : ''
  process.stderr.write(`${label} failed: ${normalized.code}: ${normalized.message}${suffix}\n`)
  if (normalized.code === 'usage_error') return 2
  if (normalized.code === 'cancelled') return cancelledBy === 'SIGINT' ? 130 : 143
  return 1
}

export async function main(argv = process.argv.slice(2)) {
  const helpRequested = argv[0] === '--help' || argv[0] === '-h' || (['export', 'verify', 'drill'].includes(argv[0]) && (argv[1] === '--help' || argv[1] === '-h'))
  if (argv.length === 0 || helpRequested) {
    process.stdout.write(usage)
    return 0
  }
  installSignalHandlers()
  const mode = argv[0]
  let options
  try {
    const parsed = parseArguments(argv)
    options = parsed.mode === 'export'
      ? await resolveExportOptions(parsed.options)
      : await resolveVerifyOptions(parsed.mode, parsed.options)
  } catch (error) {
    return reportFailure(mode, cancelledBy ? cancelledError() : error)
  }
  try {
    assertNotCancelled()
    const record = options.mode === 'export'
      ? await runExport(options)
      : options.mode === 'verify'
        ? await runVerify(options)
        : await runDrill(options)
    assertNotCancelled()
    await printRecord(record)
    return 0
  } catch (error) {
    return reportFailure(mode, error)
  }
}

const invokedDirectly = process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)
if (invokedDirectly) {
  main().then(
    (code) => {
      process.exitCode = code
    },
    (error) => {
      process.stderr.write(`export-backup failed: operation_failed: the operation could not be completed\n`)
      process.exitCode = 1
    },
  )
}
