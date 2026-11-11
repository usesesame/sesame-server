import assert from 'node:assert/strict'
import { createHash, randomUUID } from 'node:crypto'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { docker, dockerAllowFailure } from './selfhost-docker.mjs'
import { repositoryRoot } from './selfhost-doc.mjs'
import { fixtureInstallationFiles } from './selfhost-volume-check.mjs'
import { connect, expectStatus, request } from './selfhost-client.mjs'
import { redactSensitiveLog } from './selfhost-recovery.mjs'

const args = process.argv.slice(2)
assert.equal(args.length, 2, 'Use --image <candidate image>.')
assert.equal(args[0], '--image', 'Use --image <candidate image>.')
assert.ok(args[1] && !args[1].startsWith('-'), 'A candidate image is required.')
const image = args[1]
const suffix = randomUUID()
const work = mkdtempSync(join(tmpdir(), 'sesame-install-compose-'))
const ownedContainers = new Set()
const ownedVolumes = new Set()
const ownedProjects = new Map()
const profiles = [
  { name: 'local', host: null, origin: 'http://localhost:8787', proxies: [] },
  { name: 'caddy', host: 'vault.example.com', origin: 'https://vault.example.com', proxies: ['172.31.88.0/24'] },
  { name: 'tailscale', host: 'sesame.example-tailnet.ts.net', origin: 'https://sesame.example-tailnet.ts.net', proxies: ['172.31.88.0/24'] },
]
const candidateTag = 'ghcr.io/usesesame/sesame-server-selfhost:0.0.0-ci'
let ownedTag = false
let passed = 0
let failure

try {
  ownedTag = dockerAllowFailure(['image', 'inspect', '--format', '{{.Id}}', candidateTag]).status !== 0
  docker(['tag', image, candidateTag])
  for (const profile of profiles) await checkProfile(profile)
} catch (error) {
  failure = error
} finally {
  for (const [project, settings] of ownedProjects) {
    const result = dockerAllowFailure([...settings.args, 'down', '--volumes', '--remove-orphans'], settings.options)
    if (result.status !== 0) failure = new Error(`Compose cleanup failed for ${project}. ${failure?.message ?? ''}`)
  }
  for (const name of ownedContainers) {
    const result = dockerAllowFailure(['rm', '--force', name])
    if (result.status !== 0) failure = new Error(`Container cleanup failed for ${name}. ${failure?.message ?? ''}`)
  }
  for (const name of ownedVolumes) {
    const result = dockerAllowFailure(['volume', 'rm', name])
    if (result.status !== 0) failure = new Error(`Volume cleanup failed for ${name}. ${failure?.message ?? ''}`)
  }
  if (ownedTag) {
    const result = dockerAllowFailure(['image', 'rm', candidateTag])
    if (result.status !== 0) failure = new Error(`Image tag cleanup failed for ${candidateTag}. ${failure?.message ?? ''}`)
  }
  try {
    rmSync(work, { recursive: true, force: true })
  } catch (error) {
    failure = new Error(`Workspace cleanup failed. ${failure?.message ?? ''} ${error.message}`)
  }
}

if (failure) {
  console.error(`Installation Compose check failed: ${redactSensitiveLog(failure.stack ?? failure)}`)
  process.exitCode = 1
} else {
  console.log(`Installation Compose check passed: ${passed} checks.`)
}

async function checkProfile(profile) {
  const container = `sesame-install-${profile.name}-${suffix}`
  const volume = `${container}-data`
  ownedVolumes.add(volume)
  docker(['volume', 'create', volume])
  ownedContainers.add(container)
  const command = ['install', '--profile', profile.name, '--version', '0.0.0-ci', '--dir', '/data/plan', '--json']
  if (profile.host) command.push('--host', profile.host)
  const generated = docker([
    'run', '--name', container, '--network', 'none', '--read-only',
    '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges:true',
    '--memory', '256m', '--cpus', '1', '--pids-limit', '64',
    '--volume', `${volume}:/data`, image, ...command,
  ])
  const plan = JSON.parse(generated.stdout)
  const inspected = fixtureInstallationFiles(container)
  const directory = join(work, profile.name)
  mkdirSync(directory, { mode: 0o700 })
  for (const file of inspected.files) writeFileSync(join(directory, file.name), file.content, { mode: 0o600, flag: 'wx' })
  assert.deepEqual(JSON.parse(readFileSync(join(directory, 'plan.json'), 'utf8')), plan)
  assert.equal(plan.schemaVersion, 1)
  assert.equal(plan.profile, profile.name)
  assert.equal(plan.publicUrl, profile.origin)
  assert.deepEqual(plan.trustedProxies, profile.proxies)
  assert.ok(plan.prerequisites.every((item) => item.state === 'unknown'))
  const expectedFiles = profile.name === 'caddy' ? ['.env', 'Caddyfile', 'caddy.env', 'compose.yaml', 'plan.json'] : ['.env', 'compose.yaml', 'plan.json']
  assert.deepEqual(inspected.files.map((file) => file.name).sort(), expectedFiles.sort())
  assert.deepEqual(plan.files.map((file) => file.path).sort(), expectedFiles.filter((name) => name !== 'plan.json').sort())
  assert.equal(inspected.mode, 0o700)
  for (const file of inspected.files) assert.equal(file.mode, 0o600)
  for (const file of plan.files) {
    assert.ok(expectedFiles.includes(file.path) && file.path !== 'plan.json')
    assert.equal(file.sha256, createHash('sha256').update(readFileSync(join(directory, file.path))).digest('hex'))
    assert.equal(file.mode, '0600')
  }
  for (const name of profile.name === 'caddy' ? ['compose.yaml', 'Caddyfile'] : ['compose.yaml']) {
    assert.deepEqual(readFileSync(join(directory, name)), readFileSync(join(repositoryRoot, 'deploy/selfhost', name)))
  }
  passed += 1
  console.log(`ok - the image generates a private ${profile.name} plan without live data`)

  const environment = { ...process.env }
  for (const key of Object.keys(environment)) {
    if (key.startsWith('SESAME_') || key.startsWith('COMPOSE_')) delete environment[key]
  }
  const project = `sesame-install-${profile.name}-${suffix}`
  const composeArgs = ['compose', '--project-name', project, '--file', join(directory, 'compose.yaml')]
  const composeOptions = { cwd: directory, env: environment }
  const config = JSON.parse(docker([...composeArgs, 'config', '--format', 'json'], composeOptions).stdout)
  const service = config.services.sesame
  assert.equal(service.image, 'ghcr.io/usesesame/sesame-server-selfhost:0.0.0-ci')
  assert.equal(service.environment.SESAME_PUBLIC_URL, profile.origin)
  assert.equal(service.environment.SESAME_TRUSTED_PROXIES, profile.proxies.join(','))
  assert.equal(service.read_only, true)
  assert.deepEqual(service.cap_drop, ['ALL'])
  assert.ok(service.security_opt.includes('no-new-privileges:true'))
  assert.equal(service.ports.length, 1)
  assert.equal(service.ports[0].host_ip, '127.0.0.1')
  assert.equal(service.ports[0].target, 8787)
  assert.equal(String(service.ports[0].published), '8787')
  assert.equal(config.networks.default.ipam.config[0].subnet, '172.31.88.0/24')
  passed += 1
  console.log(`ok - Compose renders the ${profile.name} plan with its exact origin and container restrictions`)

  ownedProjects.set(project, { args: composeArgs, options: composeOptions })
  docker([...composeArgs, 'up', '--detach', '--wait', '--wait-timeout', '90'], composeOptions)
  const instance = connect('http://localhost:8787')
  const requestOptions = { host: new URL(profile.origin).host }
  const live = expectStatus(await request(instance, 'GET', '/livez', requestOptions), 200, 'the generated service')
  assert.equal(live.json.status, 'ok')
  const configuration = expectStatus(await request(instance, 'GET', '/config.json', requestOptions), 200, 'the generated console configuration')
  assert.equal(configuration.json.setupRequired, true)
  const diagnosis = dockerAllowFailure([...composeArgs, 'exec', '-T', 'sesame', '/sesame-server', 'doctor', '--json'], composeOptions)
  const report = JSON.parse(diagnosis.stdout)
  const actions = report.checks.filter((finding) => finding.state === 'action').map(({ id, code }) => ({ id, code }))
  assert.equal(diagnosis.status, 3, `the offline doctor reported an unexpected action: ${JSON.stringify(actions)}`)
  assert.equal(report.schemaVersion, 1)
  assert.equal(report.mode, 'offline')
  assert.equal(report.overall, 'unknown')
  assert.equal(report.config.publicUrl, profile.origin)
  assert.deepEqual(report.config.trustedProxies, profile.proxies)
  docker([...composeArgs, 'down', '--volumes', '--remove-orphans'], composeOptions)
  ownedProjects.delete(project)
  passed += 1
  console.log(`ok - the ${profile.name} plan starts a healthy server and reports its container settings through the offline doctor`)
}
