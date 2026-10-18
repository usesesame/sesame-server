import assert from 'node:assert/strict'
import net from 'node:net'
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
import { containerLogs, docker, dockerAllowFailure, run, waitUntil } from './selfhost-docker.mjs'

const imageRepository = 'ghcr.io/usesesame/sesame-server-selfhost'
const ownerName = 'upgrade-owner'
const ownerPassword = 'correct horse battery staple upgrade'
const options = parseArguments(process.argv.slice(2))
const suffix = String(process.pid)
const volume = `sesame-upgrade-${suffix}`
const containers = new Set()
const summary = []
let passed = 0

try {
  if ((await main()) !== false) console.log(`Upgrade check passed: ${passed} checks.\n${summary.join('\n')}`)
} catch (error) {
  console.error(`Upgrade check failed: ${error.stack ?? error}`)
  for (const name of containers) console.error(`\n--- ${name} log ---\n${containerLogs(name).slice(-3000)}`)
  process.exitCode = 1
} finally {
  for (const name of containers) dockerAllowFailure(['rm', '--force', name])
  dockerAllowFailure(['volume', 'rm', '--force', volume])
}

function parseArguments(args) {
  const parsed = { from: '', to: '', before: '', fromPublished: false, noneOk: false, expectVersion: '' }
  for (let index = 0; index < args.length; index += 1) {
    const arg = args[index]
    if (arg === '--from-published') parsed.fromPublished = true
    else if (arg === '--none-ok') parsed.noneOk = true
    else if (['--from', '--to', '--before', '--expect-version'].includes(arg)) parsed[arg === '--expect-version' ? 'expectVersion' : arg.slice(2)] = args[++index] ?? ''
    else throw new Error(`Unknown argument ${arg}. Use --to <ref> with --from <ref> or --from-published [--before <version>] [--none-ok], and optionally --expect-version <v>.`)
  }
  if (!parsed.to || (!parsed.from && !parsed.fromPublished)) throw new Error('--to and either --from or --from-published are required.')
  return parsed
}

async function check(name, runCheck) {
  try {
    const result = await runCheck()
    passed += 1
    console.log(`ok - ${name}`)
    return result
  } catch (error) {
    error.message = `${name}: ${error.message}`
    throw error
  }
}

function versionParts(version) {
  return version.split('.').map(Number)
}

function compareVersions(left, right) {
  const a = versionParts(left)
  const b = versionParts(right)
  for (let index = 0; index < 3; index += 1) if (a[index] !== b[index]) return a[index] - b[index]
  return 0
}

function previousPublishedImage() {
  const listed = run('gh', ['api', 'repos/usesesame/sesame-server/releases', '--paginate', '--jq', '.[].tag_name'])
  assert.equal(listed.status, 0, `gh could not list releases\n${listed.stderr}`)
  const versions = listed.stdout.split('\n').map((tag) => tag.trim().match(/^v(\d+\.\d+\.\d+)$/)?.[1]).filter(Boolean)
    .filter((version) => !options.before || compareVersions(version, options.before) < 0)
    .sort((left, right) => compareVersions(right, left))
  for (const version of versions) {
    const image = `${imageRepository}:${version}`
    if (dockerAllowFailure(['manifest', 'inspect', image]).status === 0) return { image, version }
  }
  return null
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

function startContainer(name, image, port) {
  containers.add(name)
  docker([
    'run', '--detach', '--name', name,
    '--publish', `127.0.0.1:${port}:8787`,
    '--env', `SESAME_PUBLIC_URL=http://localhost:${port}`,
    '--volume', `${volume}:/data`,
    image,
  ])
}

async function livez(instance) {
  const response = await request(instance, 'GET', '/livez', { timeout: 3000 })
  return response.status === 200 ? response.json : false
}

function stopContainer(name) {
  docker(['stop', '--time', '20', name])
  const code = Number(docker(['inspect', '--format', '{{.State.ExitCode}}', name]).stdout.trim())
  assert.equal(code, 0, `${name} did not stop cleanly`)
  docker(['rm', name])
  containers.delete(name)
}

async function main() {
  let fromImage = options.from
  let fromVersion = ''
  if (options.fromPublished) {
    const previous = previousPublishedImage()
    if (!previous && options.noneOk) {
      console.log(`::notice::No release before ${options.before || 'now'} has a published self-host image, so no upgrade was tested.`)
      console.log('Upgrade check did not run: there is no earlier published image.')
      return false
    }
    assert.ok(previous, 'No earlier release has a published self-host image to upgrade from.')
    fromImage = previous.image
    fromVersion = previous.version
  }
  console.log(`Upgrading from ${fromImage} to ${options.to}.`)
  const port = await freePort()
  const instance = connect(`http://localhost:${port}`)
  docker(['volume', 'create', volume])

  startContainer('sesame-upgrade-old', fromImage, port)
  await check('the previous image starts on a new volume', async () => {
    const live = await waitUntil('the previous image', () => livez(instance))
    if (fromVersion) assert.equal(live.version, fromVersion)
  })

  const state = await check('the previous image holds an owner, a member and a paired device', async () => {
    const token = setupTokenFrom(containerLogs('sesame-upgrade-old'), instance.origin)
    const setup = await runFirstSetup(instance, token, { name: ownerName, password: ownerPassword })
    const member = await createMember(setup.session, 'Ada Upgrade')
    const device = await pairDevice(instance, setup.session, { memberId: member.id, deviceName: 'Upgrade laptop' })
    const system = await ownerSystem(setup.session)
    return { codes: setup.codes, member, device, schema: system.schemaVersion }
  })

  await check('the previous image stops cleanly', async () => stopContainer('sesame-upgrade-old'))

  startContainer('sesame-upgrade-new', options.to, port)
  const upgraded = await check('the new image opens the same volume and keeps every record', async () => {
    const live = await waitUntil('the new image', () => livez(instance))
    if (options.expectVersion) assert.equal(live.version, options.expectVersion)
    const session = await ownerLogin(instance, { name: ownerName, password: ownerPassword, codes: state.codes })
    assert.deepEqual((await listMembers(session)).map((member) => member.name), [state.member.name])
    expectStatus(await desktopStatus(instance, state.device.token), 200, 'the device token after the upgrade')
    const system = await ownerSystem(session)
    assert.equal(system.auditChainOk, true, 'the audit chain does not verify after the upgrade')
    assert.ok(system.schemaVersion >= state.schema, 'the schema went backwards')
    return { version: live.version, schema: system.schemaVersion }
  })

  const premigration = await check('a schema change writes a backup first and an unchanged schema writes none', async () => {
    const logged = containerLogs('sesame-upgrade-new').match(/wrote a backup before upgrading the database[^\n]*path=(\S+)/)
    if (upgraded.schema > state.schema) {
      assert.ok(logged, 'the schema changed but the log shows no backup before the migration')
      assert.match(logged[1], /\/sesame-premigration-v\d+-\d{8}T\d{6}Z(?:-\d+)?\.tar$/)
      const verified = docker(['exec', 'sesame-upgrade-new', '/sesame-server', 'check', logged[1]])
      assert.match(verified.stdout, /is intact/)
      return logged[1]
    }
    assert.equal(logged, null, 'the schema did not change but the server wrote a pre-migration backup')
    return ''
  })

  await check('the new image stops cleanly', async () => stopContainer('sesame-upgrade-new'))

  startContainer('sesame-upgrade-back', fromImage, port)
  await check('rolling back to the previous image behaves as SELF-HOSTING.md says', async () => {
    if (upgraded.schema === state.schema) {
      await waitUntil('the previous image on the upgraded volume', () => livez(instance))
      const session = await ownerLogin(instance, { name: ownerName, password: ownerPassword, codes: state.codes })
      assert.deepEqual((await listMembers(session)).map((member) => member.name), [state.member.name])
      summary.push(`Schema ${state.schema} did not change, so the previous image opened the upgraded volume and served the same records.`)
      stopContainer('sesame-upgrade-back')
      return
    }
    const exit = await waitUntil('the previous image to refuse the newer database', () => {
      const status = docker(['inspect', '--format', '{{.State.Status}} {{.State.ExitCode}}', 'sesame-upgrade-back']).stdout.trim()
      return status.startsWith('exited') ? status : false
    })
    assert.notEqual(exit, 'exited 0', 'the previous image exited cleanly on a newer database')
    assert.match(containerLogs('sesame-upgrade-back'), /written by a newer Sesame server/)
    docker(['rm', 'sesame-upgrade-back'])
    containers.delete('sesame-upgrade-back')
    docker(['run', '--rm', '--volume', `${volume}:/data`, fromImage, 'restore', premigration])
    startContainer('sesame-upgrade-restored', fromImage, port)
    await waitUntil('the previous image on the restored volume', () => livez(instance))
    const session = await ownerLogin(instance, { name: ownerName, password: ownerPassword, codes: state.codes })
    assert.deepEqual((await listMembers(session)).map((member) => member.name), [state.member.name])
    summary.push(`Schema ${state.schema} became ${upgraded.schema}. The previous image refused the volume with the documented message, and restoring the pre-migration backup brought it back.`)
  })

}
