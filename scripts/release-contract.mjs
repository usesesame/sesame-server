import { appendFileSync, mkdirSync, writeFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const versionPattern = /^(?:v)?(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$/
const commitPattern = /^[0-9a-f]{40}$/
const digestReferencePattern = /^([^\s@]+)@sha256:([0-9a-f]{64})$/

export function releaseIdentity(versionInput, commitInput) {
  const match = String(versionInput).trim().match(versionPattern)
  if (!match) throw new Error('Release version must be a complete semantic version tag.')
  if (match[4]?.split('.').some(identifier => /^\d+$/.test(identifier) && identifier.length > 1 && identifier.startsWith('0'))) {
    throw new Error('Numeric prerelease identifiers must not contain leading zeroes.')
  }
  const commit = String(commitInput).trim().toLowerCase()
  if (!commitPattern.test(commit)) throw new Error('Release commit must be a full 40-character Git SHA.')
  return { version: String(versionInput).trim().replace(/^v/, ''), commit }
}

export function digestReference(value, component) {
  const match = String(value).trim().match(digestReferencePattern)
  if (!match) throw new Error(`${component} image must use an immutable sha256 digest reference.`)
  return { reference: match[0], name: match[1], digest: `sha256:${match[2]}` }
}

export function releaseBuildConfig(input) {
  const identity = releaseIdentity(input.version, input.commit)
  const origins = {}
  for (const [field, label] of [['apiOrigin', 'API'], ['siteOrigin', 'site']]) {
    const value = String(input[field] || '').trim()
    const origin = new URL(value)
    if (origin.protocol !== 'https:' || origin.origin !== value || origin.pathname !== '/') {
      throw new Error(`Release ${label} origin must be an exact HTTPS origin.`)
    }
    origins[field] = value
  }
  const sourceURL = String(input.sourceURL || '').trim()
  const source = new URL(sourceURL)
  if (source.protocol !== 'https:' || source.username || source.password || source.search || source.hash) {
    throw new Error('Release source URL must be an HTTPS repository URL.')
  }
  const capabilityPublicKey = String(input.capabilityPublicKey || '').trim()
  if (!/^[A-Za-z0-9_-]{43}$/.test(capabilityPublicKey) || Buffer.from(capabilityPublicKey, 'base64url').length !== 32) {
    throw new Error('Release capability public key must contain a base64url 32-byte Ed25519 public key.')
  }
  return { ...identity, ...origins, sourceURL, capabilityPublicKey }
}

function writeJSON(path, value) {
  const output = resolve(path)
  mkdirSync(dirname(output), { recursive: true })
  writeFileSync(output, `${JSON.stringify(value, null, 2)}\n`, 'utf8')
}

function portal(component, versionInput, commitInput, output) {
  if (component !== 'account' && component !== 'admin') throw new Error('Portal component must be account or admin.')
  writeJSON(output, { schemaVersion: 1, component, ...releaseIdentity(versionInput, commitInput) })
}

function manifest(versionInput, commitInput, apiInput, accountInput, adminInput, output) {
  const identity = releaseIdentity(versionInput, commitInput)
  writeJSON(output, {
    schemaVersion: 1,
    ...identity,
    images: {
      api: digestReference(apiInput, 'API'),
      account: digestReference(accountInput, 'Account'),
      admin: digestReference(adminInput, 'Admin'),
    },
  })
}

function github(versionInput, commitInput) {
  const output = process.env.GITHUB_OUTPUT
  if (!output) throw new Error('GITHUB_OUTPUT is required.')
  const identity = releaseIdentity(versionInput, commitInput)
  appendFileSync(output, `version=${identity.version}\ncommit=${identity.commit}\n`, 'utf8')
}

function main(args) {
  const [command, ...values] = args
  if (command === 'validate' && values.length === 2) {
    process.stdout.write(`${JSON.stringify(releaseIdentity(values[0], values[1]))}\n`)
    return
  }
  if (command === 'portal' && values.length === 4) {
    portal(...values)
    return
  }
  if (command === 'manifest' && values.length === 6) {
    manifest(...values)
    return
  }
  if (command === 'github' && values.length === 2) {
    github(...values)
    return
  }
  throw new Error('Unknown release-contract command.')
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    main(process.argv.slice(2))
  } catch (error) {
    process.stderr.write(`${error instanceof Error ? error.message : error}\n`)
    process.exitCode = 1
  }
}
