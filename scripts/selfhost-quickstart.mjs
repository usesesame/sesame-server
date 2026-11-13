import assert from 'node:assert/strict'
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import {
  connect,
  createMember,
  desktopStatus,
  expectError,
  expectStatus,
  listMembers,
  ownerLogin,
  ownerSystem,
  pairDevice,
  request,
  runFirstSetup,
  setupTokenFrom,
} from './selfhost-client.mjs'
import { dockerAllowFailure, docker, mustRun, run, runBlock, waitUntil } from './selfhost-docker.mjs'
import { readSelfHostingDocument, repositoryRoot, replaceOnce, sectionBlocks } from './selfhost-doc.mjs'
import { approvalEnvironment, assertApprovalResult, assertRecoveryStatus, parseRecoveryJson, redactSensitiveLog } from './selfhost-recovery.mjs'

const options = parseArguments(process.argv.slice(2))
const imageRepository = 'ghcr.io/usesesame/sesame-server-selfhost'
const localVersion = '0.0.0-ci'
const ownerName = 'quickstart-owner'
const ownerPassword = 'correct horse battery staple quickstart'
const origin = 'http://localhost:8787'
const workDir = mkdtempSync(join(options.tempRoot ?? tmpdir(), 'sesame-quickstart-'))
const projectDir = join(workDir, 'sesame')
const document = readSelfHostingDocument()
let passed = 0

try {
  await main()
  console.log(`Quick start check passed against ${options.mode === 'local' ? options.image : 'the published release'}: ${passed} checks.`)
} catch (error) {
  console.error(`Quick start check failed: ${redactSensitiveLog(error.stack ?? error)}`)
  for (const [directory, project] of [[projectDir, null], [join(workDir, 'fresh'), 'sesame-fresh']]) {
    if (!existsSync(directory)) continue
    const args = project ? ['compose', '-p', project, 'logs', '--no-color'] : ['compose', 'logs', '--no-color']
    const logs = dockerAllowFailure(args, { cwd: directory })
    console.error(`\n--- compose logs (${project ?? 'sesame-selfhost'}) ---\n${redactSensitiveLog((logs.stdout + logs.stderr).slice(-4000))}`)
  }
  process.exitCode = 1
} finally {
  cleanUp()
}

function parseArguments(args) {
  const parsed = { mode: '', image: '', expectVersion: '', verifyAttestations: false, tempRoot: null }
  for (let index = 0; index < args.length; index += 1) {
    const arg = args[index]
    if (['--mode', '--image', '--expect-version'].includes(arg)) parsed[arg === '--expect-version' ? 'expectVersion' : arg.slice(2)] = args[++index] ?? ''
    else if (arg === '--verify-attestations') parsed.verifyAttestations = true
    else if (arg === '--temp-root') parsed.tempRoot = resolve(args[++index] ?? '')
    else throw new Error(`Unknown argument ${arg}. Use --mode local --image <ref> or --mode published [--expect-version <v>] [--verify-attestations].`)
  }
  if (!['local', 'published'].includes(parsed.mode)) throw new Error('--mode must be local or published.')
  if (parsed.mode === 'local' && !parsed.image) throw new Error('--mode local needs --image <ref>.')
  if (parsed.verifyAttestations && parsed.mode !== 'published') throw new Error('--verify-attestations needs --mode published.')
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

function compose(args, { project, cwd = projectDir } = {}) {
  return docker(project ? ['compose', '-p', project, ...args] : ['compose', ...args], { cwd })
}

function composeEnvironment(project) {
  return project ? { ...process.env, COMPOSE_PROJECT_NAME: project } : { ...process.env }
}

function composeLogs(cwd, project) {
  const result = dockerAllowFailure(project ? ['compose', '-p', project, 'logs', '--no-color'] : ['compose', 'logs', '--no-color'], { cwd })
  return `${result.stdout}${result.stderr}`
}

function cleanUp() {
  for (const [directory, project] of [[projectDir, null], [join(workDir, 'fresh'), 'sesame-fresh']]) {
    if (!existsSync(directory)) continue
    dockerAllowFailure(project ? ['compose', '-p', project, 'down', '--volumes', '--remove-orphans'] : ['compose', 'down', '--volumes', '--remove-orphans'], { cwd: directory })
  }
  rmSync(workDir, { recursive: true, force: true })
}

function downloadBlock() {
  const blocks = sectionBlocks(document, '## Quick start')
  assert.equal(blocks.length, 2, 'the Quick start section must hold exactly the download block and the start block')
  const [download, start] = blocks
  if (options.mode === 'published') return { download, start }
  mkdirSync(workDir, { recursive: true })
  const latest = join(workDir, 'latest-release.json')
  writeFileSync(latest, `{\n  "url": "https://api.github.com/repos/usesesame/sesame-server/releases/1",\n  "tag_name": "v${localVersion}",\n  "name": "Sesame server ${localVersion}",\n  "draft": false\n}\n`)
  let rewritten = replaceOnce(download, 'https://api.github.com/repos/usesesame/sesame-server/releases/latest', `file://${latest}`, 'the release lookup address')
  rewritten = replaceOnce(rewritten, 'https://raw.githubusercontent.com/usesesame/sesame-server/v${SESAME_IMAGE_TAG}/deploy/selfhost/compose.yaml', `file://${join(repositoryRoot, 'deploy', 'selfhost', 'compose.yaml')}`, 'the compose file address')
  return { download: rewritten, start }
}

function recoveryBlocks() {
  const restore = sectionBlocks(document, '### Restore')
  assert.equal(restore.length, 1, 'the Restore section must hold exactly the restore block')
  const approve = sectionBlocks(document, '### Approve a restore')
  assert.equal(approve.length, 4, 'the Approve a restore section must hold the status, JSON status, approval and start blocks')
  const external = sectionBlocks(document, '### Restore a backup outside the data volume')
  assert.equal(external.length, 1, 'the external restore section must hold exactly the restore block')
  assert.match(approve[1], /recover --status --json/, 'the second approval block does not ask for JSON status')
  assert.match(approve[2], /--confirm "\$SESAME_RECOVERY_GENERATION"/, 'the approval block has no generation placeholder')
  assert.match(approve[2], /--retain-owner "\$SESAME_RETAIN_OWNER_ID"/, 'the approval block has no owner placeholder')
  assert.match(approve[2], /--retain-device "\$SESAME_RETAIN_DEVICE_ID"/, 'the approval block has no device placeholder')
  assert.match(external[0], /-T sesame restore - < before-trip\.tar/, 'the external restore block does not use standard input with -T')
  return { restore: restore[0], status: approve[0], statusJson: approve[1], approval: approve[2], start: approve[3], external: external[0] }
}

async function livez() {
  try {
    const response = await request(connect(origin), 'GET', '/livez', { timeout: 3000 })
    return response.status === 200 ? response.json : false
  } catch (error) {
    if (['ECONNREFUSED', 'ECONNRESET'].includes(error.code)) return false
    throw error
  }
}

async function waitForListening(label) {
  return waitUntil(label, livez)
}

async function refusePendingStart(block, { cwd, project, label }) {
  run('bash', ['-euo', 'pipefail', '-c', block], { cwd, env: composeEnvironment(project) })
  await waitUntil(label, () => /waiting for local recovery approval|recovery is pending|recover --status/i.test(composeLogs(cwd, project)), { seconds: 20 })
  assert.equal(await livez(), false, `${label}: the server answered before recovery approval`)
  compose(['stop', 'sesame'], { project, cwd })
}

function readPendingStatus(blocks, { cwd, project, label }) {
  const human = runBlock(blocks.status, { cwd, env: composeEnvironment(project) })
  const encoded = runBlock(blocks.statusJson, { cwd, env: composeEnvironment(project) })
  const status = assertRecoveryStatus(parseRecoveryJson(encoded.stdout, label), label)
  assert.equal(status.pending, true, `${label} is not pending after a restore`)
  assert.ok(human.output.includes(status.generation), `${label} human output does not name the pending generation`)
  assert.ok(human.output.includes(ownerName), `${label} human output does not list the owner`)
  assert.ok(!encoded.output.includes(ownerPassword), `${label} status output holds the owner password`)
  return status
}

function approvePendingRecovery(blocks, { cwd, project, status, ownerIds, deviceIds, label }) {
  const environment = { ...composeEnvironment(project), ...approvalEnvironment(status, { ownerIds, deviceIds }) }
  const encoded = runBlock(blocks.approval, { cwd, env: environment })
  return { environment, result: parseRecoveryJson(encoded.stdout, label) }
}

function runRejectedApproval(blocks, { cwd, project, environment, label }) {
  const rejected = run('bash', ['-euo', 'pipefail', '-c', blocks.approval], { cwd, env: { ...composeEnvironment(project), ...environment } })
  assert.notEqual(rejected.status, 0, `${label}: a stale generation approved the restore`)
}

async function main() {
  mkdirSync(workDir, { recursive: true })
  const { download, start } = downloadBlock()
  const blocks = recoveryBlocks()
  let firstRestoreGeneration = ''
  if (options.mode === 'local') docker(['tag', options.image, `${imageRepository}:${localVersion}`])

  const version = await check('the download block writes .env and compose.yaml', async () => {
    runBlock(download, { cwd: workDir })
    const env = readFileSync(join(projectDir, '.env'), 'utf8').trim()
    const match = env.match(/^SESAME_IMAGE_TAG=(\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?)$/)
    assert.ok(match, `.env holds ${JSON.stringify(env)} instead of a version`)
    const expected = options.mode === 'local' ? localVersion : options.expectVersion
    if (expected) assert.equal(match[1], expected, 'the latest release is not the version this run expected')
    const composeFile = readFileSync(join(projectDir, 'compose.yaml'), 'utf8')
    assert.ok(composeFile.includes(imageRepository))
    if (options.mode === 'local') assert.equal(composeFile, readFileSync(join(repositoryRoot, 'deploy', 'selfhost', 'compose.yaml'), 'utf8'))
    return match[1]
  })

  const setupToken = await check('the start block brings the server up, answers /livez and prints the setup link', async () => {
    const result = runBlock(start, { cwd: projectDir })
    const live = JSON.parse(result.stdout.slice(result.stdout.indexOf('{'), result.stdout.indexOf('}') + 1))
    assert.equal(live.status, 'ok')
    assert.equal(live.version, version)
    return setupTokenFrom(result.stdout, origin)
  })

  if (options.verifyAttestations) {
    await check('the verify command from the document accepts the published image index', async () => {
      const verify = sectionBlocks(document, '## Verify and build the image')[0]
      assert.ok(verify.includes('gh attestation verify'), 'the first block of the verify section does not verify an attestation')
      runBlock(verify, { cwd: projectDir })
    })

    await check('each platform image carries a signed SPDX attestation from the release workflow', async () => {
      const index = JSON.parse(docker(['buildx', 'imagetools', 'inspect', `${imageRepository}:${version}`, '--format', '{{json .Manifest}}']).stdout)
      const platforms = index.manifests.filter((entry) => entry.platform?.os === 'linux')
      assert.deepEqual(platforms.map((entry) => entry.platform.architecture).sort(), ['amd64', 'arm64'])
      for (const entry of platforms) {
        mustRun('gh', [
          'attestation', 'verify', `oci://${imageRepository}@${entry.digest}`,
          '--repo', 'usesesame/sesame-server',
          '--signer-workflow', 'usesesame/sesame-server/.github/workflows/release.yml',
          '--source-ref', `refs/tags/v${version}`,
          '--predicate-type', 'https://spdx.dev/Document/v2.3',
        ])
      }
    })
  }

  await check('the data lives in the volume the document names', async () => {
    const volumes = docker(['volume', 'ls', '--format', '{{.Name}}']).stdout.split('\n')
    assert.ok(volumes.includes('sesame-selfhost_sesame-data'), `volumes: ${volumes.join(', ')}`)
  })

  const instance = connect(origin)
  await check('the console is served with its content security policy', async () => {
    const index = expectStatus(await request(instance, 'GET', '/'), 200, 'console index')
    assert.match(index.headers['content-security-policy'] ?? '', /^default-src 'none'; script-src 'self'/)
    assert.match(index.text, /<script type="module"[^>]+src="\.\/assets\//)
  })

  const setup = await check('first-run setup creates the owner from the setup link', async () => {
    const result = await runFirstSetup(instance, setupToken, { name: ownerName, password: ownerPassword })
    const system = await ownerSystem(result.session)
    assert.equal(system.version, version)
    assert.ok(Number.isInteger(system.schemaVersion) && system.schemaVersion >= 1)
    const session = expectStatus(await result.session.call('GET', '/v1/owner/session'), 200, 'owner session')
    assert.match(session.json.owner.id, /^[0-9a-f]{32}$/)
    return { codes: result.codes, session: result.session, ownerId: session.json.owner.id }
  })

  const member = await check('the owner adds a member and pairs two devices through the API', async () => {
    const created = await createMember(setup.session, 'Ada Quickstart')
    const good = await pairDevice(instance, setup.session, { memberId: created.id, deviceName: 'Quick start laptop' })
    const bad = await pairDevice(instance, setup.session, { memberId: created.id, deviceName: 'Quick start phone' })
    assert.notEqual(good.deviceId, bad.deviceId, 'the two paired devices share an ID')
    const status = expectStatus(await desktopStatus(instance, good.token), 200, 'desktop status')
    assert.equal(status.json.connected, true)
    assert.equal(status.json.device.deviceId, good.deviceId)
    return { member: created, good, bad }
  })

  const before = await check('the instance identity and an unused pairing are recorded before the backup', async () => {
    const info = expectStatus(await request(instance, 'GET', '/v1/instance'), 200, 'instance')
    const pairing = expectStatus(await setup.session.call('POST', '/v1/owner/pairings', { json: { memberId: member.member.id, deviceName: 'Never redeemed' } }), 201, 'unused pairing')
    assert.ok(pairing.json.code.length >= 32, 'the unused pairing code is too short')
    const listed = expectStatus(await setup.session.call('GET', '/v1/owner/pairings'), 200, 'pairings before the backup')
    assert.ok(listed.json.pairings.some((entry) => entry.id === pairing.json.pairingId), 'the unused pairing is not pending before the backup')
    return { instanceId: info.json.instanceId, pairingCode: pairing.json.code }
  })

  const archive = await check('the backup commands from the document write a backup that passes check', async () => {
    const blocks = sectionBlocks(document, '### Back up')
    assert.ok(blocks.length >= 2, 'the Back up section must show the backup block and the check block')
    const backups = runBlock(blocks[0], { cwd: projectDir })
    assert.match(backups.output, /Backup written to \/data\/backups\/before-trip\.tar/)
    const checked = runBlock(blocks[1], { cwd: projectDir })
    assert.match(checked.output, /is intact/)
    return 'before-trip.tar'
  })

  await check('changes after the backup are gone and the restore waits for approval', async () => {
    const afterwards = await ownerLogin(instance, { name: ownerName, password: ownerPassword, codes: setup.codes })
    await createMember(afterwards, 'Added After The Backup')
    assert.equal((await listMembers(afterwards)).length, 2)
    expectStatus(await afterwards.call('DELETE', `/v1/owner/devices/${member.bad.deviceId}`), 204, 'revoke the second device after the backup')
    assert.ok(blocks.restore.includes(`restore /data/backups/${archive}`), 'the Restore block does not restore the backup the Back up block writes')
    runBlock(blocks.restore, { cwd: projectDir })
    await refusePendingStart(blocks.start, { cwd: projectDir, label: 'the pending file restore' })
    const status = readPendingStatus(blocks, { cwd: projectDir, label: 'the file restore' })
    firstRestoreGeneration = status.generation
    for (const secret of [member.good.token, member.bad.token, setup.session.state.cookie, before.pairingCode]) {
      assert.ok(!JSON.stringify(status).includes(secret), 'the recovery status holds a sensitive value')
    }
    assert.equal(status.owners.length, 1, 'the restored status does not hold exactly one owner')
    assert.equal(status.owners[0].id, setup.ownerId, 'the restored owner ID changed')
    assert.equal(status.owners[0].name, ownerName)
    assert.deepEqual(status.devices.map((device) => device.id).sort(), [member.good.deviceId, member.bad.deviceId].sort(), 'the restored status does not hold the two backed up devices')
    for (const device of status.devices) assert.equal(device.revokedAt, null, `the device ${device.id} was revoked in the backup`)
    const selection = { ownerIds: [setup.ownerId], deviceIds: [member.good.deviceId] }
    const approved = approvePendingRecovery(blocks, { cwd: projectDir, status, ...selection, label: 'the file restore approval' })
    assertApprovalResult(approved.result, { status, ...selection })
    const repeated = runBlock(blocks.approval, { cwd: projectDir, env: approved.environment })
    const repeatedResult = parseRecoveryJson(repeated.stdout, 'the repeated approval')
    assert.equal(repeatedResult.alreadyComplete, true, 'a repeated approval applied a selection again')
    assert.equal(repeatedResult.generation, status.generation, 'the repeated approval names another generation')
    assert.equal(repeatedResult.pending, false)
    runBlock(blocks.start, { cwd: projectDir })
    await waitForListening('the restored server')
    const restored = await ownerLogin(instance, { name: ownerName, password: ownerPassword, codes: setup.codes })
    const session = expectStatus(await restored.call('GET', '/v1/owner/session'), 200, 'restored owner session')
    assert.equal(session.json.owner.id, setup.ownerId, 'the restored owner signed in with another ID')
    assert.deepEqual((await listMembers(restored)).map((entry) => entry.name), [member.member.name])
    expectStatus(await desktopStatus(instance, member.good.token), 200, 'the retained device token')
    expectError(await desktopStatus(instance, member.bad.token), 401, 'not_authenticated', 'the device revoked before the restore')
    expectError(await request(instance, 'GET', '/v1/owner/session', { headers: { cookie: setup.session.state.cookie } }), 401, 'session_expired', 'the owner session from before the restore')
    expectError(await request(instance, 'POST', '/v1/desktop/link', { json: { code: before.pairingCode, deviceName: 'Never redeemed' } }), 401, 'invalid_desktop_link', 'the pairing from before the restore')
    const info = expectStatus(await request(instance, 'GET', '/v1/instance'), 200, 'restored instance')
    assert.equal(info.json.instanceId, before.instanceId, 'the restored instance ID changed')
  })

  await check('the private backup restores through standard input into a fresh volume and waits for approval', async () => {
    const fresh = join(workDir, 'fresh')
    mkdirSync(fresh)
    const copy = join(fresh, archive)
    compose(['cp', `sesame:/data/backups/${archive}`, copy])
    assert.ok(existsSync(copy))
    chmodSync(copy, 0o600)
    compose(['down'])
    writeFileSync(join(fresh, 'compose.yaml'), readFileSync(join(projectDir, 'compose.yaml')))
    writeFileSync(join(fresh, '.env'), readFileSync(join(projectDir, '.env')))
    assert.ok(blocks.external.includes(`-T sesame restore - < ${archive}`), 'the external Restore block does not pipe the backup the Back up block writes on standard input')
    assert.ok(!blocks.external.includes('chmod 0644'), 'the external Restore block makes the backup readable to other accounts')
    assert.equal(statSync(copy).mode & 0o777, 0o600, 'the backup is not private before the restore')
    const project = 'sesame-fresh'
    runBlock(blocks.external, { cwd: fresh, env: composeEnvironment(project) })
    assert.equal(statSync(copy).mode & 0o777, 0o600, 'the restore changed the backup permissions')
    const status = readPendingStatus(blocks, { cwd: fresh, project, label: 'the standard input restore' })
    assert.notEqual(status.generation, firstRestoreGeneration, 'the second restore kept the first generation')
    const stale = approvalEnvironment({ ...status, generation: firstRestoreGeneration }, { ownerIds: [setup.ownerId], deviceIds: [member.good.deviceId] })
    runRejectedApproval(blocks, { cwd: fresh, project, environment: stale, label: 'the stale approval' })
    const stillPending = readPendingStatus(blocks, { cwd: fresh, project, label: 'the standard input restore after the stale approval' })
    assert.equal(stillPending.generation, status.generation, 'a stale approval changed the pending generation')
    for (const secret of [member.good.token, member.bad.token, setup.session.state.cookie, before.pairingCode]) {
      assert.ok(!JSON.stringify(stillPending).includes(secret), 'the recovery status holds a sensitive value')
    }
    assert.equal(stillPending.owners[0]?.id, setup.ownerId, 'the fresh restore changed the owner ID')
    assert.deepEqual(stillPending.devices.map((device) => device.id).sort(), [member.good.deviceId, member.bad.deviceId].sort(), 'the fresh restore changed the device IDs')
    await refusePendingStart(blocks.start, { cwd: fresh, project, label: 'the pending standard input restore' })
    const selection = { ownerIds: [setup.ownerId], deviceIds: [member.good.deviceId] }
    const approved = approvePendingRecovery(blocks, { cwd: fresh, project, status: stillPending, ...selection, label: 'the standard input restore approval' })
    assertApprovalResult(approved.result, { status: stillPending, ...selection })
    runBlock(blocks.start, { cwd: fresh, env: composeEnvironment(project) })
    await waitForListening('the server on the fresh volume')
    const volumes = docker(['volume', 'ls', '--format', '{{.Name}}']).stdout.split('\n')
    assert.ok(volumes.includes('sesame-fresh_sesame-data'), 'the restore did not use a new volume')
    const restored = await ownerLogin(instance, { name: ownerName, password: ownerPassword, codes: setup.codes })
    const session = expectStatus(await restored.call('GET', '/v1/owner/session'), 200, 'the fresh owner session')
    assert.equal(session.json.owner.id, setup.ownerId, 'the fresh restore changed the owner ID')
    assert.deepEqual((await listMembers(restored)).map((entry) => entry.name), [member.member.name])
    expectStatus(await desktopStatus(instance, member.good.token), 200, 'the retained device token on the fresh volume')
    expectError(await desktopStatus(instance, member.bad.token), 401, 'not_authenticated', 'the device revoked before the fresh restore')
    const info = expectStatus(await request(instance, 'GET', '/v1/instance'), 200, 'the fresh instance')
    assert.equal(info.json.instanceId, before.instanceId, 'the fresh restore changed the instance ID')
  })
}
