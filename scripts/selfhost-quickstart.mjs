import assert from 'node:assert/strict'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import {
  connect,
  createMember,
  desktopStatus,
  expectStatus,
  listMembers,
  ownerLogin,
  ownerSystem,
  pairDevice,
  request,
  runFirstSetup,
  setupTokenFrom,
} from './selfhost-client.mjs'
import { dockerAllowFailure, docker, mustRun, runBlock, waitUntil } from './selfhost-docker.mjs'
import { readSelfHostingDocument, repositoryRoot, replaceOnce, sectionBlocks } from './selfhost-doc.mjs'

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
  console.error(`Quick start check failed: ${error.stack ?? error}`)
  for (const [directory, project] of [[projectDir, null], [join(workDir, 'fresh'), 'sesame-fresh']]) {
    if (!existsSync(directory)) continue
    const args = project ? ['compose', '-p', project, 'logs', '--no-color'] : ['compose', 'logs', '--no-color']
    const logs = dockerAllowFailure(args, { cwd: directory })
    console.error(`\n--- compose logs (${project ?? 'sesame-selfhost'}) ---\n${(logs.stdout + logs.stderr).slice(-4000)}`)
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

async function livez() {
  const response = await request(connect(origin), 'GET', '/livez', { timeout: 3000 })
  return response.status === 200 ? response.json : false
}

async function main() {
  mkdirSync(workDir, { recursive: true })
  const { download, start } = downloadBlock()
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
    return { codes: result.codes, session: result.session }
  })

  const member = await check('the owner adds a member and pairs a device through the API', async () => {
    const created = await createMember(setup.session, 'Ada Quickstart')
    const paired = await pairDevice(instance, setup.session, { memberId: created.id, deviceName: 'Quick start laptop' })
    const status = expectStatus(await desktopStatus(instance, paired.token), 200, 'desktop status')
    assert.equal(status.json.connected, true)
    return { member: created, device: paired }
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

  await check('a change made after the backup is gone once the restore commands from the document run', async () => {
    const afterwards = await ownerLogin(instance, { name: ownerName, password: ownerPassword, codes: setup.codes })
    await createMember(afterwards, 'Added After The Backup')
    assert.equal((await listMembers(afterwards)).length, 2)
    const restore = sectionBlocks(document, '### Restore')[0]
    assert.ok(restore.includes(`restore /data/backups/${archive}`), 'the Restore block does not restore the backup the Back up block writes')
    runBlock(restore, { cwd: projectDir })
    await waitUntil('the restored server', livez)
    const restored = await ownerLogin(instance, { name: ownerName, password: ownerPassword, codes: setup.codes })
    assert.deepEqual((await listMembers(restored)).map((entry) => entry.name), [member.member.name])
    expectStatus(await desktopStatus(instance, member.device.token), 200, 'the device token after restore')
  })

  await check('the backup restores into a fresh volume and the data reads back', async () => {
    const fresh = join(workDir, 'fresh')
    mkdirSync(fresh)
    const copy = join(fresh, archive)
    compose(['cp', `sesame:/data/backups/${archive}`, copy])
    assert.ok(existsSync(copy))
    compose(['down'])
    writeFileSync(join(fresh, 'compose.yaml'), readFileSync(join(projectDir, 'compose.yaml')))
    writeFileSync(join(fresh, '.env'), readFileSync(join(projectDir, '.env')))
    const freshArgs = { project: 'sesame-fresh', cwd: fresh }
    compose(['run', '--rm', '--no-deps', '-v', `${copy}:/restore.tar:ro`, 'sesame', 'restore', '/restore.tar'], freshArgs)
    compose(['up', '-d', '--wait'], freshArgs)
    await waitUntil('the server on the fresh volume', livez)
    const volumes = docker(['volume', 'ls', '--format', '{{.Name}}']).stdout.split('\n')
    assert.ok(volumes.includes('sesame-fresh_sesame-data'), 'the restore did not use a new volume')
    const restored = await ownerLogin(instance, { name: ownerName, password: ownerPassword, codes: setup.codes })
    assert.deepEqual((await listMembers(restored)).map((entry) => entry.name), [member.member.name])
    expectStatus(await desktopStatus(instance, member.device.token), 200, 'the device token on the fresh volume')
  })
}
