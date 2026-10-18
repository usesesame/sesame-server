import { spawnSync } from 'node:child_process'
import { releaseIdentity } from './release-contract.mjs'

const identity = releaseIdentity(process.env.SESAME_RELEASE_VERSION, process.env.SESAME_RELEASE_COMMIT)
const sourceURL = process.env.SESAME_RELEASE_SOURCE_URL || 'https://github.com/usesesame/sesame-server'
const updatePublicKeys = process.env.SESAME_UPDATE_PUBLIC_KEYS ?? ''
const platforms = [
  ['linux/amd64', required('SELFHOST_AMD64_IMAGE')],
  ['linux/arm64', required('SELFHOST_ARM64_IMAGE')],
]

for (const [, image] of platforms) {
  if (/\s|@/.test(image)) throw new Error('Self-host build image names must be mutable local tags before publication.')
}

for (const [platform, image] of platforms) build(platform, image)

function required(name) {
  const value = process.env[name]?.trim()
  if (!value) throw new Error(`${name} is required.`)
  return value
}

function build(platform, image) {
  const result = spawnSync('docker', [
    'build', '--file', 'Dockerfile.selfhost', '--platform', platform, '--tag', image,
    '--build-arg', `SESAME_VERSION=${identity.version}`,
    '--build-arg', `SESAME_COMMIT=${identity.commit}`,
    '--build-arg', `SESAME_SOURCE_URL=${sourceURL}`,
    '--build-arg', `SESAME_UPDATE_PUBLIC_KEYS=${updatePublicKeys}`,
    '.',
  ], { stdio: 'inherit' })
  if (result.status !== 0) process.exit(result.status ?? 1)
}
