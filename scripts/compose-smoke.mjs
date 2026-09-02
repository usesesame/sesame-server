import { spawnSync } from 'node:child_process'
import { releaseIdentity } from './release-contract.mjs'

const identity = releaseIdentity(process.env.SESAME_RELEASE_VERSION, process.env.SESAME_RELEASE_COMMIT)
const images = {
  api: required('SESAME_API_IMAGE'),
  account: required('SESAME_ACCOUNT_IMAGE'),
  admin: required('SESAME_ADMIN_IMAGE'),
}
const project = `sesame-release-smoke-${process.pid}`
const file = 'deploy/compose/compose.release-smoke.yaml'

for (const [component, image] of Object.entries(images)) verifyImage(component, image)

let started = false
try {
  started = true
  compose(['up', '--detach', '--wait', '--wait-timeout', '150'])
  await verifyJSON('api', 8787, '/livez', { status: 'ok', version: identity.version, commit: identity.commit })
  await verifyJSON('account', 8080, '/release.json', { schemaVersion: 1, component: 'account', ...identity })
  await verifyJSON('admin', 8080, '/release.json', { schemaVersion: 1, component: 'admin', ...identity })
} finally {
  if (started) compose(['down', '--volumes', '--remove-orphans'], false)
}

function required(name) {
  const value = process.env[name]?.trim()
  if (!value) throw new Error(`${name} is required.`)
  return value
}

function compose(args, fail = true) {
  const result = spawnSync('docker', ['compose', '--project-name', project, '--file', file, ...args], { stdio: 'inherit', env: process.env })
  if (fail && result.status !== 0) throw new Error(`Docker Compose failed with status ${result.status}.`)
}

function verifyImage(component, image) {
  const result = spawnSync('docker', ['image', 'inspect', '--format', '{{json .Config.Labels}}', image], { encoding: 'utf8' })
  if (result.status !== 0) throw new Error(`${component} image is unavailable locally.`)
  const labels = JSON.parse(result.stdout)
  if (labels['org.opencontainers.image.version'] !== identity.version || labels['org.opencontainers.image.revision'] !== identity.commit) {
    throw new Error(`${component} image identity does not match the release.`)
  }
}

async function verifyJSON(service, containerPort, path, expected) {
  const portResult = spawnSync('docker', ['compose', '--project-name', project, '--file', file, 'port', service, String(containerPort)], { encoding: 'utf8', env: process.env })
  if (portResult.status !== 0) throw new Error(`Could not resolve the ${service} smoke port.`)
  const port = portResult.stdout.trim().split(':').at(-1)
  const response = await fetch(`http://127.0.0.1:${port}${path}`, { signal: AbortSignal.timeout(5000) })
  if (!response.ok) throw new Error(`${service} smoke request returned ${response.status}.`)
  const actual = await response.json()
  for (const [key, value] of Object.entries(expected)) {
    if (actual[key] !== value) throw new Error(`${service} ${key} does not match the release.`)
  }
}
