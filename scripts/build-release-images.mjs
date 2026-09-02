import { spawnSync } from 'node:child_process'
import { releaseBuildConfig } from './release-contract.mjs'

const config = releaseBuildConfig({
  version: process.env.SESAME_RELEASE_VERSION,
  commit: process.env.SESAME_RELEASE_COMMIT,
  apiOrigin: process.env.SESAME_RELEASE_API_ORIGIN,
  siteOrigin: process.env.SESAME_RELEASE_SITE_ORIGIN,
  sourceURL: process.env.SESAME_RELEASE_SOURCE_URL || 'https://github.com/usesesame/sesame-server',
  capabilityPublicKey: process.env.SESAME_RELEASE_CAPABILITY_PUBLIC_KEY,
})
const apiImage = required('SESAME_API_IMAGE')
const accountImage = required('SESAME_ACCOUNT_IMAGE')
const adminImage = required('SESAME_ADMIN_IMAGE')
const buildNetwork = process.env.SESAME_DOCKER_BUILD_NETWORK?.trim()

if (buildNetwork && buildNetwork !== 'default' && buildNetwork !== 'host') {
  throw new Error('SESAME_DOCKER_BUILD_NETWORK must be default or host.')
}

for (const image of [apiImage, accountImage, adminImage]) {
  if (/\s|@/.test(image)) throw new Error('Release build image names must be mutable local tags before publication.')
}

build('Dockerfile', apiImage, [
  'SESAME_VERSION', config.version,
  'SESAME_COMMIT', config.commit,
  'SESAME_SOURCE_URL', config.sourceURL,
])
build('web/account/Dockerfile', accountImage, [
  'SESAME_VERSION', config.version,
  'SESAME_COMMIT', config.commit,
  'SESAME_SOURCE_URL', config.sourceURL,
  'VITE_SESAME_API_URL', config.apiOrigin,
  'VITE_SESAME_SITE_ORIGIN', config.siteOrigin,
  'VITE_SESAME_CAPABILITY_PUBLIC_KEY', config.capabilityPublicKey,
])
build('web/admin/Dockerfile', adminImage, [
  'SESAME_VERSION', config.version,
  'SESAME_COMMIT', config.commit,
  'SESAME_SOURCE_URL', config.sourceURL,
  'VITE_SESAME_API_URL', config.apiOrigin,
])

function required(name) {
  const value = process.env[name]?.trim()
  if (!value) throw new Error(`${name} is required.`)
  return value
}

function build(file, image, entries) {
  const args = ['build', '--file', file, '--tag', image]
  if (buildNetwork) args.push('--network', buildNetwork)
  for (let index = 0; index < entries.length; index += 2) {
    args.push('--build-arg', `${entries[index]}=${entries[index + 1]}`)
  }
  args.push('.')
  const result = spawnSync('docker', args, { stdio: 'inherit' })
  if (result.status !== 0) process.exit(result.status ?? 1)
}
