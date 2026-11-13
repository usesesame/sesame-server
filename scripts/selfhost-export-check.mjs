import assert from 'node:assert/strict'
import { spawn, spawnSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, statSync, writeFileSync } from 'node:fs'
import net from 'node:net'
import { tmpdir } from 'node:os'
import { basename, dirname, isAbsolute, join, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { connect, createMember, desktopStatus, expectStatus, pairDevice, request, runFirstSetup, setupTokenFrom } from './selfhost-client.mjs'
import { mustRun, waitUntil } from './selfhost-docker.mjs'
import { approvalArguments, assertApprovalResult, assertRecoveryStatus, parseRecoveryJson, redactSensitiveLog } from './selfhost-recovery.mjs'

const repositoryRoot = dirname(dirname(fileURLToPath(import.meta.url)))
const operatorScript = join(repositoryRoot, 'scripts', 'export-backup.mjs')
const quickstartScript = join(repositoryRoot, 'scripts', 'selfhost-quickstart.mjs')
const ownerName = 'export-owner'
const ownerPassword = 'correct horse battery staple export'
const memberName = 'Ada Export'
const fictionalCommit = 'fedcba9876543210fedcba9876543210fedcba98'
const archiveFields = ['auditHeadHash', 'auditRows', 'hasConfig', 'needsMigration', 'ownersVerified', 'schemaVersion', 'secretsVerified', 'supported']

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main(process.argv.slice(2)).catch((error) => {
    console.error(`Encrypted backup check failed: ${redactSensitiveLog(error.stack ?? error)}`)
    process.exitCode = 1
  })
}

async function main(args) {
  const options = parseArguments(args)
  if (options.mode === 'docker') {
    const tools = resolveAgeTools(options)
    const result = spawnSync(process.execPath, [
      quickstartScript,
      '--mode', 'local',
      '--image', options.image,
      '--encrypted-backup',
      '--age', tools.age,
      '--age-keygen', tools.ageKeygen,
      ...(options.tempRoot ? ['--temp-root', options.tempRoot] : []),
    ], { stdio: 'inherit' })
    if (result.error) throw result.error
    if (result.status !== 0) throw new Error(`the Docker quick start exited ${result.status}`)
    return
  }
  await runNative(options)
}

function parseArguments(args) {
  const parsed = { mode: '', image: '', age: '', ageKeygen: '', binary: '', skipConsole: false, tempRoot: '', keep: false }
  for (let index = 0; index < args.length; index += 1) {
    const arg = args[index]
    if (arg === '--mode') parsed.mode = args[++index] ?? ''
    else if (arg === '--image') parsed.image = args[++index] ?? ''
    else if (arg === '--age') parsed.age = executable(args[++index] ?? '')
    else if (arg === '--age-keygen') parsed.ageKeygen = executable(args[++index] ?? '')
    else if (arg === '--binary') parsed.binary = resolve(args[++index] ?? '')
    else if (arg === '--skip-console-build') parsed.skipConsole = true
    else if (arg === '--temp-root') parsed.tempRoot = resolve(args[++index] ?? '')
    else if (arg === '--keep') parsed.keep = true
    else throw new Error(`Unknown argument ${arg}. Use --mode native|docker, --image <ref>, --age <path>, --age-keygen <path>, --binary <path>, --skip-console-build, --temp-root <dir> or --keep.`)
  }
  if (!['native', 'docker'].includes(parsed.mode)) throw new Error('--mode must be native or docker.')
  if (parsed.mode === 'docker' && !parsed.image) throw new Error('--mode docker needs --image <ref>.')
  if (parsed.mode === 'native' && parsed.image) throw new Error('--image is only accepted with --mode docker.')
  if (parsed.mode === 'docker' && parsed.binary) throw new Error('--binary is only accepted with --mode native.')
  return parsed
}

export function resolveAgeTools({ age = '', ageKeygen = '' } = {}) {
  const tools = { age: age ? executable(age) : 'age', ageKeygen: ageKeygen ? executable(ageKeygen) : 'age-keygen' }
  for (const [name, command] of [['age', tools.age], ['age-keygen', tools.ageKeygen]]) {
    const result = spawnSync(command, ['--version'], { encoding: 'utf8' })
    if (result.error) throw new Error(`${name} v1.3.2 is required for the real-age checks: ${result.error.message}`)
    assert.equal(result.status, 0, `${name} --version exited ${result.status}`)
    assert.equal(result.stdout.trim(), 'v1.3.2', `${name} reports ${result.stdout.trim()} instead of v1.3.2`)
  }
  return tools
}

function executable(value) {
  return existsSync(value) ? resolve(value) : value
}

async function runNative(options) {
  const tools = resolveAgeTools(options)
  const workDir = mkdtempSync(join(options.tempRoot ? resolve(options.tempRoot) : tmpdir(), 'sesame-export-native-'))
  let server = null
  let passed = 0
  const check = async (name, action) => {
    try {
      const value = await action()
      passed += 1
      console.log(`ok - ${name}`)
      return value
    } catch (error) {
      error.message = `${name}: ${error.message}`
      throw error
    }
  }
  try {
    await check('the private age tools are v1.3.2', async () => tools)
    if (!options.binary) {
      if (options.skipConsole) assert.ok(existsSync(join(repositoryRoot, 'web', 'console', 'dist', 'index.html')), 'web/console/dist is missing, so --skip-console-build cannot be used')
      else await check('the console builds for the native fixture', async () => buildConsole())
    }
    const binary = await check('the server builds for the native fixture', async () => buildBinary(options, workDir))
    const fixture = await check('a fictional owner and device are set up on the native fixture', async () => {
      server = await startServer(binary, join(workDir, 'source-data'))
      return createFixture(server, binary)
    })
    const sourceBefore = sourceState(fixture.dataDir)
    const journey = await check('encrypted export, offline verify and a disposable drill use real age', async () => runEncryptedJourney({
      label: 'the native encrypted backup check',
      age: tools.age,
      ageKeygen: tools.ageKeygen,
      source: { kind: 'native', binary, dataDir: fixture.dataDir, cwd: repositoryRoot },
      target: { kind: 'native', binary },
      destination: join(workDir, 'exports'),
      recoveryDir: join(workDir, 'recovery'),
      workParent: join(workDir, 'offline-work'),
      stateDir: join(workDir, 'state'),
      negative: true,
      expectedSchema: 6,
      secrets: [ownerPassword, fixture.device.token, fixture.ownerCookie],
    }))
    await check('the encrypted archive restores the same instance and only the explicitly retained device', async () => {
      await assertEncryptedRecovery(binary, fixture, journey, tools, workDir)
    })
    await check('the drill leaves the live source files and the running service unchanged', async () => {
      assert.deepEqual(sourceState(fixture.dataDir), sourceBefore, 'the encrypted backup operations changed the live source files')
      const instance = connect(server.origin)
      const status = expectStatus(await desktopStatus(instance, fixture.device.token), 200, 'the live device after the encrypted backup check')
      assert.equal(status.json.device.deviceId, fixture.device.deviceId, 'the live device changed after the encrypted backup check')
      const info = expectStatus(await request(instance, 'GET', '/v1/instance'), 200, 'the live instance after the encrypted backup check')
      assert.equal(info.json.instanceId, fixture.instanceId, 'the live instance changed after the encrypted backup check')
    })
    console.log(`Encrypted backup check passed: ${passed} checks.`)
  } catch (error) {
    if (server) console.error(`\n--- setup server log ---\n${redactSensitiveLog(server.log().slice(-4000))}`)
    throw error
  } finally {
    if (server) await server.stop().catch(() => {})
    if (options.keep) console.log(`Kept ${workDir}`)
    else rmSync(workDir, { recursive: true, force: true })
  }
}

function buildConsole() {
  const consoleRoot = join(repositoryRoot, 'web', 'console')
  if (!existsSync(join(consoleRoot, 'node_modules'))) mustRun('npm', ['ci'], { cwd: consoleRoot })
  mustRun('npm', ['run', 'build'], { cwd: consoleRoot })
}

function buildBinary(options, workDir) {
  if (options.binary) {
    assert.ok(existsSync(options.binary), `${options.binary} does not exist`)
    return options.binary
  }
  const consoleRoot = join(repositoryRoot, 'web', 'console')
  const embedRoot = join(repositoryRoot, 'internal', 'selfhost', 'console', 'dist')
  const replacement = {}
  const walk = (directory) => {
    for (const entry of readdirSync(directory, { withFileTypes: true })) {
      const path = join(directory, entry.name)
      if (entry.isDirectory()) walk(path)
      else replacement[join(embedRoot, relative(join(consoleRoot, 'dist'), path))] = path
    }
  }
  assert.ok(existsSync(join(consoleRoot, 'dist', 'index.html')), 'web/console/dist is missing, so the native fixture cannot build')
  walk(join(consoleRoot, 'dist'))
  const overlay = join(workDir, 'embed-overlay.json')
  writeFileSync(overlay, JSON.stringify({ Replace: replacement }))
  const output = join(workDir, 'sesame-server')
  const ldflags = `-s -w -X usesesame.app/backend/internal/buildinfo.Version=0.0.0-export -X usesesame.app/backend/internal/buildinfo.Commit=${fictionalCommit}`
  mustRun('go', ['build', '-trimpath', '-overlay', overlay, `-ldflags=${ldflags}`, '-o', output, './cmd/sesame-server'], {
    cwd: repositoryRoot,
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

function serverEnvironment(dataDir, port) {
  const environment = Object.fromEntries(Object.entries(process.env).filter(([name]) => !name.startsWith('SESAME_')))
  return {
    ...environment,
    SESAME_DATA_DIR: dataDir,
    SESAME_ADDR: `127.0.0.1:${port}`,
    SESAME_PUBLIC_URL: `http://localhost:${port}`,
    SESAME_BACKUP_INTERVAL: '0',
    SESAME_LOG_LEVEL: 'info',
  }
}

async function startServer(binary, dataDir) {
  const port = await freePort()
  const child = spawn(binary, ['serve'], { env: serverEnvironment(dataDir, port), stdio: ['ignore', 'pipe', 'pipe'] })
  let output = ''
  child.stdout.on('data', (chunk) => { output += chunk })
  child.stderr.on('data', (chunk) => { output += chunk })
  const exited = new Promise((resolveExit) => child.once('exit', (code, signal) => resolveExit({ code, signal })))
  const server = {
    port,
    origin: `http://localhost:${port}`,
    dataDir,
    log: () => output,
    exited,
    async stop() {
      if (child.exitCode !== null || child.signalCode !== null) return exited
      child.kill('SIGTERM')
      return exited
    },
  }
  try {
    await waitUntil('the native fixture server', () => output.includes('Sesame server listening'), { seconds: 30, interval: 50 })
  } catch (error) {
    child.kill('SIGKILL')
    throw new Error(`${error.message}\n${output.slice(-2000)}`)
  }
  return server
}

async function createFixture(server, binary) {
  const instance = connect(server.origin)
  let setupToken = ''
  await waitUntil('the first-run setup link', () => {
    try {
      setupToken = setupTokenFrom(server.log(), server.origin)
      return true
    } catch {
      return false
    }
  }, { seconds: 20, interval: 50 })
  const setup = await runFirstSetup(instance, setupToken, { name: ownerName, password: ownerPassword })
  const member = await createMember(setup.session, memberName)
  const device = await pairDevice(instance, setup.session, { memberId: member.id, deviceName: 'Export laptop' })
  const omittedDevice = await pairDevice(instance, setup.session, { memberId: member.id, deviceName: 'Other export laptop' })
  const owner = expectStatus(await setup.session.call('GET', '/v1/owner/session'), 200, 'owner before the encrypted backup').json.owner
  const info = expectStatus(await request(instance, 'GET', '/v1/instance'), 200, 'instance before the encrypted backup').json
  const health = spawnSync(binary, ['healthcheck'], { env: serverEnvironment(server.dataDir, server.port), encoding: 'utf8' })
  assert.equal(health.status, 0, health.stderr)
  return {
    instanceId: info.instanceId,
    fingerprint: info.fingerprint,
    ownerId: owner.id,
    ownerCookie: setup.session.state.cookie,
    member,
    device,
    omittedDevice,
    dataDir: server.dataDir,
  }
}

async function assertEncryptedRecovery(binary, fixture, journey, tools, workDir) {
  const target = join(workDir, 'explicit-recovery')
  mkdirPrivate(target)
  let restored = null
  try {
    const decrypted = spawnSync(tools.age, ['--decrypt', '--identity', journey.identityFile, journey.first.publication.path], {
      maxBuffer: 16 * 1024 * 1024,
      timeout: 30000,
    })
    assert.equal(decrypted.error, undefined, 'the recovery decryption process failed')
    assert.equal(decrypted.status, 0, 'the recovery identity did not authenticate the complete export')
    const environment = serverEnvironment(target, await freePort())
    const restore = spawnSync(binary, ['restore', '-'], {
      env: environment, input: decrypted.stdout, encoding: 'utf8', timeout: 30000,
    })
    assert.equal(restore.status, 0, 'the authenticated encrypted archive did not restore')
    const refused = spawnSync(binary, ['serve'], { env: environment, encoding: 'utf8', timeout: 10000 })
    assert.equal(refused.status, 1, 'the restored server did not refuse startup before approval')
    assert.match(refused.stderr, /recovery approval/, 'startup failed without naming pending recovery')
    const statusResult = spawnSync(binary, ['recover', '--status', '--json'], { env: environment, encoding: 'utf8', timeout: 30000 })
    assert.equal(statusResult.status, 0, 'the restored recovery status could not be read')
    const status = assertRecoveryStatus(parseRecoveryJson(statusResult.stdout, 'encrypted recovery status'))
    assert.equal(status.pending, true, 'the encrypted restore did not need approval')
    assert.deepEqual(status.owners.map((owner) => owner.id), [fixture.ownerId], 'the encrypted restore changed the owner identity')
    assert.deepEqual(status.devices.map((device) => device.id).sort(), [fixture.device.deviceId, fixture.omittedDevice.deviceId].sort(), 'the encrypted restore changed the backed up device identities')
    const selection = { ownerIds: [fixture.ownerId], deviceIds: [fixture.device.deviceId] }
    const approval = spawnSync(binary, approvalArguments(status, selection), { env: environment, encoding: 'utf8', timeout: 30000 })
    assert.equal(approval.status, 0, 'the explicit encrypted recovery selection failed')
    assertApprovalResult(parseRecoveryJson(approval.stdout, 'encrypted recovery approval'), { status, ...selection })
    restored = await startServer(binary, target)
    const instance = connect(restored.origin)
    const info = expectStatus(await request(instance, 'GET', '/v1/instance'), 200, 'the explicitly recovered instance').json
    assert.equal(info.instanceId, fixture.instanceId, 'the encrypted recovery changed the instance identity')
    assert.equal(info.fingerprint, fixture.fingerprint, 'the encrypted recovery changed the server fingerprint')
    const retained = expectStatus(await desktopStatus(instance, fixture.device.token), 200, 'the explicitly retained device')
    assert.equal(retained.json.device.deviceId, fixture.device.deviceId, 'the retained device changed identity')
    expectStatus(await desktopStatus(instance, fixture.omittedDevice.token), 401, 'the device omitted from encrypted recovery')
    expectStatus(await request(instance, 'GET', '/v1/owner/session', { headers: { cookie: fixture.ownerCookie } }), 401, 'the restored historical owner session')
  } finally {
    if (restored) await restored.stop()
    rmSync(target, { recursive: true, force: true })
  }
}

function sourceState(dataDir) {
  const entries = []
  const walk = (directory) => {
    for (const entry of readdirSync(directory, { withFileTypes: true }).sort((left, right) => left.name.localeCompare(right.name))) {
      const path = join(directory, entry.name)
      if (entry.isDirectory()) walk(path)
      else if (entry.isFile() && entry.name !== 'sesame.lock' && !/-(?:wal|shm)$/.test(entry.name)) {
        const stat = statSync(path)
        entries.push({
          path: relative(dataDir, path),
          mode: stat.mode & 0o777,
          bytes: stat.size,
          digest: sha256(readFileSync(path)),
        })
      }
    }
  }
  walk(dataDir)
  return {
    files: entries,
    digest: sha256(entries.map((entry) => `${entry.path}\0${entry.mode.toString(8)}\0${entry.bytes}\0${entry.digest}`).join('\n')),
  }
}

export async function runEncryptedJourney(options) {
  const {
    age,
    ageKeygen,
    source,
    target,
    destination,
    recoveryDir,
    workParent,
    stateDir,
    negative = false,
    expectedSchema = 6,
    secrets = [],
  } = options
  assert.ok(existsSync(operatorScript), `the encrypted backup operator tool is missing at ${operatorScript}`)
  for (const directory of [destination, recoveryDir, workParent, stateDir]) mkdirPrivate(directory)
  const identityFile = join(recoveryDir, 'identity.txt')
  const wrongIdentityFile = join(recoveryDir, 'wrong-identity.txt')
  const recipientsFile = join(recoveryDir, 'recipients.txt')
  mustRun(ageKeygen, ['-o', identityFile])
  mustRun(ageKeygen, ['-o', wrongIdentityFile])
  chmodSync(identityFile, 0o600)
  chmodSync(wrongIdentityFile, 0o600)
  const recipient = mustRun(ageKeygen, ['-y', identityFile]).stdout.trim()
  assert.match(recipient, /^age1[0-9a-z]+$/, 'age-keygen did not print a public recipient')
  writeFileSync(recipientsFile, `${recipient}\n`, { mode: 0o600 })
  chmodSync(recipientsFile, 0o600)
  const identitySecret = readFileSync(identityFile, 'utf8').trim()
  const allSecrets = [...secrets, identitySecret, identityFile, wrongIdentityFile, recipient]

  const previousName = 'sesame-20260101T000000Z-abcdef.tar.age'
  const previousPath = join(destination, previousName)
  writeFileSync(previousPath, 'fictional previous export\n', { mode: 0o600 })
  const previousDigest = sha256(readFileSync(previousPath))
  const scratch = join(recoveryDir, 'scratch')
  mkdirPrivate(scratch)

  const sourceArgs = source.kind === 'compose'
    ? ['--compose-service', source.service, ...(source.composeFile ? ['--compose-file', source.composeFile] : [])]
    : ['--native', source.binary, '--data-dir', source.dataDir]
  const targetArgs = target.kind === 'image' ? ['--image', target.image] : ['--native', target.binary]
  const exportArgs = ['export', ...sourceArgs, '--destination', destination, '--recipients-file', recipientsFile, '--state-dir', stateDir, '--age', age]
  const verifyArgs = (archive, identity) => ['verify', '--archive', archive, '--identity-file', identity, ...targetArgs, '--work-dir', join(workParent, 'verify'), '--state-dir', stateDir, '--age', age]
  const drillArgs = (archive, identity) => ['drill', '--archive', archive, '--identity-file', identity, ...targetArgs, '--work-dir', join(workParent, 'drill'), '--state-dir', stateDir, '--age', age]
  mkdirPrivate(join(workParent, 'verify'))
  mkdirPrivate(join(workParent, 'drill'))

  const first = parseRecord(runOperator(exportArgs, { cwd: source.cwd, label: 'the encrypted export' }), 'export')
  assertExportRecord(first, { secrets: allSecrets })
  const firstPath = first.publication.path
  assert.ok(isAbsolute(firstPath), 'the published export path is not absolute')
  assert.equal(dirname(firstPath), resolve(destination), 'the published export is outside the destination')
  if (process.platform !== 'win32') assert.equal(statSync(firstPath).mode & 0o777, 0o600, 'the published export is not mode 0600')
  assert.equal(sha256(readFileSync(firstPath)), digestOf(first.encryption, 'ciphertext'), 'the ciphertext digest does not match the published file')
  assert.equal(sha256(readFileSync(previousPath)), previousDigest, 'the export changed an older published file')

  const second = parseRecord(runOperator(exportArgs, { cwd: source.cwd, label: 'the second encrypted export' }), 'export')
  assertExportRecord(second, { secrets: allSecrets })
  assert.notEqual(second.publication.path, firstPath, 'two exports reused one published name')
  assert.equal(sha256(readFileSync(previousPath)), previousDigest, 'a later export changed an older published file')
  assert.equal(sha256(readFileSync(firstPath)), digestOf(first.encryption, 'ciphertext'), 'a later export changed the first published file')
  assert.deepEqual(publishedExports(destination).sort(), [previousName, basename(firstPath), basename(second.publication.path)].sort(), 'the destination no longer holds exactly the older export and the two new exports')
  assert.equal(readdirSync(destination).filter((name) => name.includes('.part-')).length, 0, 'a ciphertext staging file was left behind')

  if (negative) {
    const refusedIdentity = runOperator([...exportArgs, '--identity-file', identityFile], { cwd: source.cwd, label: 'the export identity refusal', allowFailure: true })
    assert.notEqual(refusedIdentity.status, 0, 'the export accepted a private identity')
    assertNoSuccessJson(refusedIdentity, 'the export identity refusal', allSecrets)
    const wrong = runOperator(verifyArgs(firstPath, wrongIdentityFile), { label: 'the wrong identity verify', allowFailure: true })
    assert.notEqual(wrong.status, 0, 'verify accepted the wrong identity')
    assertNoSuccessJson(wrong, 'the wrong identity verify', allSecrets)
    const corrupted = join(scratch, 'corrupt-final.tar.age')
    const bytes = Buffer.from(readFileSync(firstPath))
    bytes[bytes.length - 1] ^= 0xff
    writeFileSync(corrupted, bytes, { mode: 0o600 })
    const corruptedResult = runOperator(verifyArgs(corrupted, identityFile), { label: 'the corrupted final chunk verify', allowFailure: true })
    assert.notEqual(corruptedResult.status, 0, 'verify accepted a corrupted final chunk')
    assertNoSuccessJson(corruptedResult, 'the corrupted final chunk verify', allSecrets)
    const malformed = join(scratch, 'not-an-archive.tar.age')
    const plaintext = join(scratch, 'not-an-archive.bin')
    writeFileSync(plaintext, Buffer.from('fictional plaintext that is not an archive\n'), { mode: 0o600 })
    mustRun(age, ['--encrypt', '-R', recipientsFile, '-o', malformed, plaintext])
    const malformedResult = runOperator(verifyArgs(malformed, identityFile), { label: 'the malformed archive verify', allowFailure: true })
    assert.notEqual(malformedResult.status, 0, 'verify accepted an age stream that was not a valid archive')
    assertNoSuccessJson(malformedResult, 'the malformed archive verify', allSecrets)
    const wrongDrill = runOperator(drillArgs(firstPath, wrongIdentityFile), { label: 'the wrong identity drill', allowFailure: true })
    assert.notEqual(wrongDrill.status, 0, 'the drill accepted the wrong identity')
    assertNoSuccessJson(wrongDrill, 'the wrong identity drill', allSecrets)
    assert.equal(sha256(readFileSync(previousPath)), previousDigest, 'a failed operation changed an older published file')
    assert.equal(sha256(readFileSync(firstPath)), digestOf(first.encryption, 'ciphertext'), 'a failed operation changed the published export')
  }

  const verified = parseRecord(runOperator(verifyArgs(firstPath, identityFile), { label: 'the offline verify' }), 'verify')
  assertVerifyRecord(verified, { archiveDigest: sha256(readFileSync(firstPath)), expectedSchema, secrets: allSecrets })

  const drilled = parseRecord(runOperator(drillArgs(firstPath, identityFile), { label: 'the disposable drill' }), 'drill')
  assertDrillRecord(drilled, { secrets: allSecrets })
  for (const directory of [join(workParent, 'verify'), join(workParent, 'drill')]) {
    const leftovers = existsSync(directory) ? readdirSync(directory) : []
    assert.equal(leftovers.length, 0, `${basename(directory)} workspace was not removed`)
  }

  const decrypted = await hashDecrypted(age, identityFile, firstPath)
  assert.equal(decrypted, digestOf(first.snapshot, 'archive'), 'the recorded snapshot digest does not cover the decrypted archive')
  const history = readHistory(stateDir, allSecrets, expectedSchema)
  for (const operation of ['export', 'verify', 'drill']) {
    assert.ok(history.some((record) => record.operation === operation), `the operation history has no ${operation} record`)
  }
  return { first, second, verified, drilled, history, recipient, identityFile }
}

function runOperator(args, { cwd = repositoryRoot, label = 'the encrypted backup command', allowFailure = false } = {}) {
  const environment = Object.fromEntries(Object.entries(process.env).filter(([name]) => !['AGE_IDENTITY', 'AGE_AGENT', 'SSH_AUTH_SOCK'].includes(name)))
  const result = spawnSync(process.execPath, [operatorScript, ...args], {
    encoding: 'utf8',
    cwd,
    env: environment,
    maxBuffer: 16 * 1024 * 1024,
    timeout: 180000,
  })
  if (result.error) throw result.error
  if (!allowFailure && result.status !== 0) {
    throw new Error(`${label} exited ${result.status}: ${redactSensitiveLog(result.stderr.trim().slice(0, 1000))}`)
  }
  return result
}

function parseRecord(result, operation) {
  assert.equal(result.status, 0, `${operation} exited ${result.status}: ${redactSensitiveLog(result.stderr.trim().slice(0, 1000))}`)
  const text = result.stdout.trim()
  assert.ok(text, `${operation} printed no JSON record`)
  let record
  try {
    record = JSON.parse(text)
  } catch {
    assert.fail(`${operation} stdout is not one JSON record`)
  }
  assert.ok(record && typeof record === 'object' && !Array.isArray(record), `${operation} JSON is not an object`)
  return record
}

function assertNoSuccessJson(result, label, secrets) {
  const text = result.stdout.trim()
  if (text) {
    let value
    try {
      value = JSON.parse(text)
    } catch {
      value = null
    }
    assert.notEqual(value?.success, true, `${label} printed a success record`)
  }
  assert.ok(result.stderr.trim(), `${label} printed no error`)
  assert.ok(result.stderr.length <= 4000, `${label} printed an unbounded error`)
  assertNoSecrets(result.stderr, secrets, `${label} stderr`)
}

function assertNoSecrets(value, secrets, label) {
  const text = typeof value === 'string' ? value : JSON.stringify(value)
  for (const secret of secrets) {
    if (!secret) continue
    assert.ok(!text.includes(secret), `${label} holds a private identity, recipient or credential value`)
  }
  assert.ok(!text.includes('AGE-SECRET-KEY-'), `${label} holds a private age key`)
}

export function assertExportRecord(record, { secrets = [] } = {}) {
  assertCommonRecord(record, 'export')
  assert.ok(record.snapshot && typeof record.snapshot === 'object', 'the export record has no snapshot facts')
  assert.ok(record.encryption && typeof record.encryption === 'object', 'the export record has no encryption facts')
  assert.ok(record.publication && typeof record.publication === 'object', 'the export record has no publication facts')
  assert.equal(record.verification, null, 'the export record carries verification facts')
  assert.equal(record.drill, null, 'the export record carries drill facts')
  assertTime(factTime(record.snapshot), 'the snapshot success time')
  assert.ok(Number.isInteger(factBytes(record.snapshot)) && factBytes(record.snapshot) > 0, 'the snapshot byte count is not positive')
  assert.match(factDigest(record.snapshot), /^[0-9a-f]{64}$/, 'the snapshot digest is not a SHA-256')
  assertTime(factTime(record.encryption), 'the encryption success time')
  assert.ok(Number.isInteger(factBytes(record.encryption)) && factBytes(record.encryption) > 0, 'the ciphertext byte count is not positive')
  assert.match(factDigest(record.encryption), /^[0-9a-f]{64}$/, 'the ciphertext digest is not a SHA-256')
  assert.notEqual(factDigest(record.snapshot), factDigest(record.encryption), 'the snapshot and ciphertext digests are the same')
  assert.equal(typeof record.publication.path, 'string', 'the publication has no path')
  assert.ok(isAbsolute(record.publication.path), 'the published path is not absolute')
  assert.equal(record.publication.readbackConfirmed, true, 'the publication was not read back')
  assert.ok(record.publication.sameFilesystemAsSource === null || typeof record.publication.sameFilesystemAsSource === 'boolean', 'the same filesystem fact is not a boolean or null')
  assertNoSecrets(record, secrets, 'the export record')
  return record
}

export function assertArchiveCheckReport(report, { expectedSchema = 6 } = {}) {
  assert.ok(report && typeof report === 'object' && !Array.isArray(report), 'the archive check report is not an object')
  assert.deepEqual(Object.keys(report).sort(), archiveFields, 'the archive check report does not hold exactly the expected fields')
  assert.ok(Number.isInteger(report.schemaVersion) && report.schemaVersion > 0, 'the report has no valid schemaVersion')
  if (expectedSchema !== null) assert.equal(report.schemaVersion, expectedSchema, 'the report names another schema')
  assert.ok(Number.isInteger(report.supported) && report.supported >= report.schemaVersion, 'the report has no valid supported schema')
  assert.equal(typeof report.needsMigration, 'boolean', 'the report has no needsMigration boolean')
  assert.ok(Number.isInteger(report.auditRows) && report.auditRows >= 0, 'the report has no valid auditRows')
  assert.equal(typeof report.auditHeadHash, 'string', 'the report has no auditHeadHash')
  assert.match(report.auditHeadHash, /^[0-9a-f]{64}$/, 'the report audit head is not a SHA-256')
  if (report.auditRows === 0) assert.equal(report.auditHeadHash, '0'.repeat(64), 'the empty audit report does not have the genesis hash')
  assert.ok(Number.isInteger(report.ownersVerified) && report.ownersVerified >= 0, 'the report has no valid ownersVerified')
  assert.equal(typeof report.secretsVerified, 'boolean', 'the report has no secretsVerified boolean')
  assert.equal(typeof report.hasConfig, 'boolean', 'the report has no hasConfig boolean')
  return report
}

export function assertVerifyRecord(record, { archiveDigest, expectedSchema = 6, secrets = [] } = {}) {
  assertCommonRecord(record, 'verify')
  assert.ok(record.verification && typeof record.verification === 'object', 'the verify record has no verification facts')
  const checkedDigest = record.verification.archiveDigest ?? record.verification.archiveSha256
  assert.match(checkedDigest, /^[0-9a-f]{64}$/, 'the verification has no archive digest')
  if (archiveDigest) assert.equal(checkedDigest, archiveDigest, 'the verification names another archive digest')
  assertTime(record.verification.checkedAt)
  assertArchiveCheckReport(record.verification.report, { expectedSchema })
  assertNoSecrets(record, secrets, 'the verify record')
  return record
}

export function assertDrillRecord(record, { secrets = [] } = {}) {
  assertCommonRecord(record, 'drill')
  assert.ok(record.drill && typeof record.drill === 'object', 'the drill record has no drill facts')
  assertTime(record.drill.restoredAt)
  assert.equal(record.drill.recoveryPending, true, 'the drill did not report a pending recovery')
  assert.equal(record.drill.serviceProbed, false, 'the drill started or probed a network service')
  assertNoSecrets(record, secrets, 'the drill record')
  return record
}

function assertCommonRecord(record, operation) {
  assert.equal(record.operation, operation, `the record operation is not ${operation}`)
  assertTime(record.startedAt)
  assertTime(record.completedAt)
  assert.ok(Date.parse(record.completedAt) >= Date.parse(record.startedAt), 'the record completed before it started')
  assert.equal(record.success, true, 'a successful command printed a failure record')
  assert.ok(record.errorCode === null || record.errorCode === '', 'a successful command printed an error code')
  for (const field of ['snapshot', 'encryption', 'publication', 'verification', 'drill']) {
    assert.ok(field in record, `the record has no ${field} field`)
  }
}

function readHistory(stateDir, secrets, expectedSchema) {
  const records = []
  for (const entry of readdirSync(stateDir, { withFileTypes: true })) {
    if (!entry.isFile()) continue
    const path = join(stateDir, entry.name)
    if (process.platform !== 'win32') {
      assert.equal(statSync(path).mode & 0o077, 0, `${entry.name} is open to other users`)
      assert.equal(statSync(stateDir).mode & 0o077, 0, 'the state directory is open to other users')
    }
    const record = JSON.parse(readFileSync(path, 'utf8'))
    assert.ok(record && typeof record === 'object' && !Array.isArray(record), `${entry.name} is not a JSON object`)
    assert.equal(typeof record.operation, 'string', `${entry.name} has no operation`)
    assert.equal(typeof record.success, 'boolean', `${entry.name} has no success boolean`)
    assertNoSecrets(record, secrets, `${entry.name}`)
    if (record.success && record.operation === 'export') assertExportRecord(record, { secrets })
    else if (record.success && record.operation === 'verify') assertVerifyRecord(record, { expectedSchema, secrets })
    else if (record.success && record.operation === 'drill') assertDrillRecord(record, { secrets })
    records.push(record)
  }
  return records
}

function factTime(fact) {
  return fact.completedAt ?? fact.at ?? fact.time ?? fact.successAt ?? fact.publishedAt
}

function factBytes(fact) {
  return fact.bytes ?? fact.byteCount ?? fact.size ?? fact.sizeBytes
}

function factDigest(fact) {
  return fact.digest ?? fact.sha256 ?? fact.archiveDigest ?? fact.ciphertextDigest ?? fact.archiveSha256 ?? fact.ciphertextSha256
}

function digestOf(fact, label) {
  const digest = factDigest(fact)
  assert.match(digest, /^[0-9a-f]{64}$/, `the ${label} digest is not a SHA-256`)
  return digest
}

function assertTime(value, label = 'the record time') {
  assert.equal(typeof value, 'string', `${label} is not a time string`)
  assert.ok(!Number.isNaN(Date.parse(value)), `${label} is not a valid time`)
}

function publishedExports(destination) {
  return readdirSync(destination).filter((name) => name.endsWith('.tar.age'))
}

function mkdirPrivate(directory) {
  mkdirSync(directory, { recursive: true, mode: 0o700 })
  if (process.platform !== 'win32') chmodSync(directory, 0o700)
}

function sha256(bytes) {
  return createHash('sha256').update(bytes).digest('hex')
}

function hashDecrypted(age, identityFile, archive) {
  return new Promise((resolveHash, reject) => {
    const child = spawn(age, ['--decrypt', '-i', identityFile, archive], { stdio: ['ignore', 'pipe', 'pipe'] })
    const hash = createHash('sha256')
    child.stdout.on('data', (chunk) => hash.update(chunk))
    child.stderr.on('data', () => {})
    child.once('error', reject)
    child.once('close', (code) => {
      if (code === 0) return resolveHash(hash.digest('hex'))
      reject(new Error(`age did not decrypt the published export: exit ${code}`))
    })
  })
}
