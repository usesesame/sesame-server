import { createHash, randomBytes } from 'node:crypto'
import { createWriteStream } from 'node:fs'
import { link, unlink } from 'node:fs/promises'
import { createGunzip, createGzip } from 'node:zlib'
import { Readable, Transform } from 'node:stream'
import { pipeline } from 'node:stream/promises'
import { dirname, join } from 'node:path'
import { digestReference, releaseIdentity } from './release-contract.mjs'

const IMAGE_FIELDS = [
  ['api', 'SESAME_API_IMAGE', 'API'],
  ['account', 'SESAME_ACCOUNT_IMAGE', 'Account'],
  ['admin', 'SESAME_ADMIN_IMAGE', 'Admin'],
]

const RELEASE_REPOSITORY = 'usesesame/sesame-server'
const RELEASE_WORKFLOW = '.github/workflows/release.yml'

const PENDING_FILE = 'pending.json'
const STATE_FILE = 'deployed.json'
const STAGING_ENV = 'deploy-candidate.env'
export const BACKUP_MARKER = '-- PostgreSQL database dump'

export function parseRelease(bytes) {
  const text = Buffer.from(bytes).toString('utf8')
  const raw = JSON.parse(text)
  if (raw?.schemaVersion !== 1) throw new Error('The server release manifest must carry schemaVersion 1.')
  const identity = releaseIdentity(raw.version, raw.commit)
  const images = {}
  for (const [component, , label] of IMAGE_FIELDS) {
    images[component] = parseReleaseImage(raw.images?.[component], label)
  }
  return { ...identity, images, setDigest: createHash('sha256').update(text).digest('hex') }
}

function parseReleaseImage(value, label) {
  const named = typeof value === 'object' && value !== null
  const image = digestReference(named ? value.reference : value, label)
  if (named && (value.name !== image.name || value.digest !== image.digest)) {
    throw new Error(`${label} image must carry a name and digest that match its digest reference.`)
  }
  return image
}

function versionParts(version) {
  const match = String(version).match(/^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$/)
  if (!match) throw new Error(`Version ${version} is not a supported release version.`)
  return { numbers: [Number(match[1]), Number(match[2]), Number(match[3])], prerelease: match[4] ?? null }
}

function comparePrerelease(left, right) {
  if (left === right) return 0
  if (left === null) return 1
  if (right === null) return -1
  const a = left.split('.')
  const b = right.split('.')
  for (let index = 0; index < Math.max(a.length, b.length); index += 1) {
    const x = a[index]
    const y = b[index]
    if (x === undefined) return -1
    if (y === undefined) return 1
    const xNumeric = /^\d+$/.test(x)
    const yNumeric = /^\d+$/.test(y)
    if (xNumeric && yNumeric) {
      const difference = Number(x) - Number(y)
      if (difference) return Math.sign(difference)
    } else if (xNumeric) return -1
    else if (yNumeric) return 1
    else if (x !== y) return x < y ? -1 : 1
  }
  return 0
}

export function compareVersions(left, right) {
  const a = versionParts(left)
  const b = versionParts(right)
  for (let index = 0; index < 3; index += 1) {
    if (a.numbers[index] !== b.numbers[index]) return Math.sign(a.numbers[index] - b.numbers[index])
  }
  return comparePrerelease(a.prerelease, b.prerelease)
}

export function rewriteEnvImages(text, images) {
  const seen = new Set()
  const rewritten = text.split('\n').map((line) => {
    const match = line.match(/^(SESAME_(?:API|ACCOUNT|ADMIN)_IMAGE)=(.*)$/)
    if (!match) return line
    const component = match[1] === 'SESAME_API_IMAGE' ? 'api' : match[1] === 'SESAME_ACCOUNT_IMAGE' ? 'account' : 'admin'
    if (seen.has(component)) throw new Error(`${match[1]} appears more than once in the production env file.`)
    seen.add(component)
    return `${match[1]}=${images[component].reference}`
  })
  for (const [component, field] of IMAGE_FIELDS) {
    if (!seen.has(component)) throw new Error(`The production env file does not set ${field}, so the deploy tool would not own that image.`)
  }
  return rewritten.join('\n')
}

export function rehearsalRoleBootstrapSql() {
  return [
    `CREATE ROLE sesame_owner LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;`,
    `CREATE ROLE sesame_app LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;`,
    `CREATE ROLE sesame_backup LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;`,
    `GRANT pg_read_all_data TO sesame_backup;`,
    `GRANT CREATE, USAGE ON SCHEMA public TO sesame_owner;`,
    ``,
  ].join('\n')
}

// The rehearsal resolves the production API environment through `compose config`.
// `docker run --env-file` takes literal `NAME=value` lines, so a value that spans
// lines cannot be represented and must fail loudly instead of being truncated.
export function rehearsalEnvFile(apiEnvironment, scratchURL) {
  const lines = []
  for (const [name, value] of Object.entries(apiEnvironment ?? {})) {
    if (name === 'DATABASE_URL' || value === null || value === undefined) continue
    const text = String(value)
    if (text.includes('\n') || text.includes('\r')) throw new Error(`The API environment variable ${name} spans multiple lines and cannot be rehearsed through an env file.`)
    lines.push(`${name}=${text}`)
  }
  lines.push(`DATABASE_URL=${scratchURL}`)
  return `${lines.join('\n')}\n`
}

export async function assertUsableBackup(gzipBytes) {
  if (!Buffer.isBuffer(gzipBytes) || gzipBytes.length < 1024 || gzipBytes[0] !== 0x1f || gzipBytes[1] !== 0x8b) {
    throw new Error('The pre-deployment backup is not a usable gzip dump.')
  }
  const prefix = await decompressedPrefix(gzipBytes, 64 * 1024)
  if (!prefix.includes(BACKUP_MARKER)) throw new Error('The pre-deployment backup does not contain a PostgreSQL dump.')
  return createHash('sha256').update(gzipBytes).digest('hex')
}

export async function writeCompressedBackup(source, destination, confirmSource = async () => {}) {
  const staging = `${destination}.part-${randomBytes(12).toString('hex')}`
  const digest = createHash('sha256')
  let prefix = Buffer.alloc(0)
  let bytes = 0
  try {
    await pipeline(
      source,
      new Transform({
        transform(chunk, encoding, callback) {
          if (prefix.length < 64 * 1024) {
            prefix = Buffer.concat([prefix, chunk.subarray(0, 64 * 1024 - prefix.length)])
          }
          callback(null, chunk)
        },
      }),
      createGzip(),
      new Transform({
        transform(chunk, encoding, callback) {
          digest.update(chunk)
          bytes += chunk.length
          callback(null, chunk)
        },
      }),
      createWriteStream(staging, { flags: 'wx', mode: 0o600 }),
    )
    await confirmSource()
    if (bytes < 1024) throw new Error('The pre-deployment backup is not a usable gzip dump.')
    if (!prefix.includes(BACKUP_MARKER)) throw new Error('The pre-deployment backup does not contain a PostgreSQL dump.')
    await link(staging, destination)
    return { sha256: digest.digest('hex'), bytes }
  } finally {
    try {
      await unlink(staging)
    } catch (error) {
      if (error.code !== 'ENOENT') throw error
    }
  }
}

async function decompressedPrefix(gzipBytes, limit) {
  const gunzip = createGunzip()
  const source = Readable.from([gzipBytes])
  const chunks = []
  let collected = 0
  source.pipe(gunzip)
  for await (const chunk of gunzip) {
    chunks.push(chunk)
    collected += chunk.length
    if (collected >= limit) {
      gunzip.destroy()
      break
    }
  }
  return Buffer.concat(chunks)
}

export async function readDeployedState(io, root) {
  try {
    const state = JSON.parse(await io.readText(join(root, STATE_FILE)))
    if (state?.schemaVersion !== 1 || !Array.isArray(state.history)) throw new Error('bad state')
    return state
  } catch (error) {
    if (error.code === 'ENOENT' || String(error.message) === 'bad state') return { schemaVersion: 1, current: null, history: [] }
    throw error
  }
}

async function readPending(io, root) {
  try {
    const pending = JSON.parse(await io.readText(join(root, PENDING_FILE)))
    if (pending?.schemaVersion !== 1 || typeof pending.phase !== 'string') throw new Error('bad pending')
    return pending
  } catch (error) {
    if (error.code === 'ENOENT' || String(error.message) === 'bad pending') return null
    throw error
  }
}

export function classifyDeployment(state, release) {
  if (!state.current) return { action: 'bootstrap' }
  const comparison = compareVersions(release.version, state.current.version)
  if (comparison === 0) {
    return state.current.setDigest === release.setDigest ? { action: 'noop' } : { action: 'conflict' }
  }
  if (comparison < 0) return { action: 'stale' }
  return { action: 'deploy' }
}

function identityOf(record) {
  return { version: record.version, commit: record.commit, setDigest: record.setDigest, images: record.images, backup: record.backup ?? null }
}

function deployedRecord(entry, at) {
  return { ...identityOf(entry), deployedAt: at, previous: null }
}

function snapshotVersionFor(state) {
  return state.current?.version ?? 'pre-bootstrap'
}

function snapshotPathFor(root, version) {
  return join(root, 'history', version, 'env.production')
}

function stamp(at) {
  return at.replaceAll(/[-:]/g, '').replace('T', '-').replace(/\..+$/, '')
}

async function savePending(io, root, pending) {
  await io.writeJSONAtomic(join(root, PENDING_FILE), pending, 0o600)
}

export function attestationTarget(release, component) {
  const image = release.images[component]
  const match = image.name.match(/^ghcr\.io\/([^/]+)\/([^/]+)$/)
  if (!match) {
    throw new Error(`The ${component} image ${image.name} is not hosted on ghcr.io, so its provenance cannot be verified.`)
  }
  const suffix = `-${component}`
  if (match[2].length <= suffix.length || !match[2].endsWith(suffix)) {
    throw new Error(`The ${component} image ${image.name} does not follow the release repository naming of a ${suffix} image, so its provenance cannot be verified.`)
  }
  const repository = `${match[1]}/${match[2].slice(0, -suffix.length)}`
  if (repository !== RELEASE_REPOSITORY) {
    throw new Error(`The ${component} image ${image.name} names the release repository ${repository}, not the pinned repository ${RELEASE_REPOSITORY}, so its provenance cannot be verified.`)
  }
  return {
    reference: image.reference,
    repository: RELEASE_REPOSITORY,
    signerWorkflow: `${RELEASE_REPOSITORY}/${RELEASE_WORKFLOW}`,
    sourceRef: `refs/tags/v${release.version}`,
  }
}

export function attestationArgs({ reference, repository, signerWorkflow, sourceRef }) {
  return ['attestation', 'verify', `oci://${reference}`, '--repo', repository, '--signer-workflow', signerWorkflow, '--source-ref', sourceRef, '--deny-self-hosted-runners']
}

async function verifyImages(io, release) {
  const targets = {}
  for (const [component] of IMAGE_FIELDS) targets[component] = attestationTarget(release, component)
  for (const [component] of IMAGE_FIELDS) {
    const expected = release.images[component]
    await io.pullImage(expected.reference)
    const image = await io.inspectImage(expected.reference)
    if (!image) throw new Error(`Image ${expected.reference} is unavailable after pulling. Check the host registry login.`)
    if (!image.repoDigests.some((entry) => entry.endsWith(`@${expected.digest}`))) {
      throw new Error(`Image ${expected.reference} does not carry digest ${expected.digest} locally.`)
    }
    if (image.labels['org.opencontainers.image.version'] !== release.version || image.labels['org.opencontainers.image.revision'] !== release.commit) {
      throw new Error(`The ${component} image identity does not match release ${release.version} at ${release.commit}.`)
    }
    const target = targets[component]
    await io.verifyImageAttestation(target)
  }
}

function recordedRelease(record, version) {
  const images = {}
  for (const [component, , label] of IMAGE_FIELDS) {
    const value = record.images?.[component]
    if (value === undefined || value === null) {
      throw new Error(`The deployment record for ${version} predates image attestations and carries no image digests, so the rollback cannot verify provenance. Restore ${version} by hand or roll back only to a revision recorded with image digests.`)
    }
    images[component] = parseReleaseImage(value, label)
  }
  return { version, commit: record.commit, images }
}

// The pinned env file is what every later restart uses, so a deployment is not
// finished until its image lines carry the release digests. A rollback or a
// converged switch can otherwise leave the old digests in place, and the next
// plain restart silently downgrades the stack.
async function convergePinnedEnv(io, prodEnvPath, release) {
  const envText = await io.readText(prodEnvPath)
  const staged = rewriteEnvImages(envText, release.images)
  if (staged === envText) return
  await io.writeText(prodEnvPath, staged, 0o600)
  const up = await io.composeUp()
  if (!up.ok) {
    throw new Error(`The pinned env drifted from the deployed release and the restart failed: ${up.error}. The env now pins ${release.version}; bring the stack up manually to converge.`)
  }
  const live = await io.liveHealth()
  if (!live.ok || live.version !== release.version || live.commit !== release.commit) {
    throw new Error(`The pinned env drifted from the deployed release and the restarted stack does not serve ${release.version}.`)
  }
}

export async function deployRelease(io, { root, prodEnvPath, release }) {
  const state = await readDeployedState(io, root)
  let record = await readPending(io, root)
  if (record && record.release.setDigest !== release.setDigest) {
    throw new Error(`A deployment of ${record.release.version} is still pending from a different manifest. Resolve it (or remove deploy/state/${PENDING_FILE}) before deploying ${release.version}.`)
  }
  const classification = classifyDeployment(state, release)
  if (classification.action === 'noop') {
    if (record) {
      await io.unlink(join(root, PENDING_FILE))
      return { deployed: release.version, backup: state.current.backup, from: state.current.previous?.version ?? null }
    }
    await verifyImages(io, release)
    await convergePinnedEnv(io, prodEnvPath, release)
    throw new Error(`Release ${release.version} is already the deployed revision.`)
  }
  if (classification.action === 'stale') {
    throw new Error(`Release ${release.version} is older than the deployed ${state.current.version}. Deploy a newer release or roll back deliberately.`)
  }
  if (classification.action === 'conflict') {
    throw new Error(`Release ${release.version} was already deployed from a different manifest. Artifact identity is immutable; publish a new version instead.`)
  }

  const envText = await io.readText(prodEnvPath)
  const resumed = Boolean(record)
  await verifyImages(io, release)

  if (!resumed) {
    const snapshotVersion = snapshotVersionFor(state)
    const snapshotPath = snapshotPathFor(root, snapshotVersion)
    if (!(await io.exists(snapshotPath))) {
      await io.mkdirp(dirname(snapshotPath))
      await io.writeText(snapshotPath, envText, 0o600)
    }
    record = { schemaVersion: 1, phase: 'prepared', release, startedAt: io.now() }
    await savePending(io, root, record)
  }

  if (!record.backup || !(await io.exists(record.backup.file))) {
    const file = join(root, 'backups', `sesame-${release.version}-${stamp(io.now())}.sql.gz`)
    await io.mkdirp(dirname(file))
    const backup = await io.takeBackup(file)
    record.backup = { file, sha256: backup.sha256, bytes: backup.bytes }
    await savePending(io, root, record)
  }

  if (record.rehearsal?.ok !== true) {
    const previousRef = state.current?.images?.api?.reference ?? null
    const rehearsal = await io.rehearse({ backupFile: record.backup.file, candidateRef: release.images.api.reference, previousRef })
    if (!rehearsal.ok) {
      record.rehearsal = { ok: false, error: rehearsal.error, at: io.now() }
      await savePending(io, root, record)
      throw new Error(`Migration rehearsal failed before promotion: ${rehearsal.error}. The previous revision is untouched; fix the cause and retry.`)
    }
    record.rehearsal = { ok: true, at: io.now() }
    await savePending(io, root, record)
  }

  const stagingPath = join(root, STAGING_ENV)
  await io.mkdirp(root)
  await io.writeText(stagingPath, rewriteEnvImages(envText, release.images), 0o600)

  const migrations = await io.runMigrations(stagingPath)
  if (!migrations.ok) {
    throw new Error(`Migrations failed before the traffic switch: ${migrations.error}. The previous revision is still serving; resolve and retry the deploy.`)
  }

  const candidate = await io.candidateHealth(stagingPath)
  if (!candidate.ok) {
    throw new Error(`Candidate health check failed before the traffic switch: ${candidate.error}. The previous revision is still serving; resolve and retry the deploy.`)
  }
  // A healthy candidate can still be the wrong revision: a stale local image or
  // an env rewrite bug would pass the probe. Compare identity before any switch.
  if (candidate.version !== release.version || candidate.commit !== release.commit) {
    throw new Error(`The candidate reports ${candidate.version} at ${candidate.commit} instead of ${release.version} at ${release.commit}. The previous revision is still serving; resolve and retry the deploy.`)
  }
  record.phase = 'checked'
  await savePending(io, root, record)

  const liveBefore = await io.liveHealth()
  if (liveBefore.ok && liveBefore.version === release.version && liveBefore.commit === release.commit) {
    await convergePinnedEnv(io, prodEnvPath, release)
    return completeDeployment(io, root, state, release, record)
  }

  record.phase = 'switching'
  await savePending(io, root, record)
  if (!(await io.exists(stagingPath))) {
    await io.writeText(stagingPath, rewriteEnvImages(await io.readText(prodEnvPath), release.images), 0o600)
  }
  const switched = await io.switchTraffic(stagingPath)
  if (!switched.ok) return recoverFailedSwitch(io, root, prodEnvPath, state, release, record, `traffic switch failed (${switched.error})`)

  const live = await io.liveHealth()
  if (!live.ok) return recoverFailedSwitch(io, root, prodEnvPath, state, release, record, `health check after the switch failed (${live.error})`)
  if (live.version !== release.version || live.commit !== release.commit) {
    return recoverFailedSwitch(io, root, prodEnvPath, state, release, record, `the live API reports ${live.version} at ${live.commit} instead of ${release.version} at ${release.commit}`)
  }
  record.phase = 'switched'
  await savePending(io, root, record)
  return completeDeployment(io, root, state, release, record)
}

async function completeDeployment(io, root, state, release, record) {
  const from = state.current?.version ?? null
  const at = io.now()
  const next = {
    schemaVersion: 1,
    current: {
      ...identityOf({ version: release.version, commit: release.commit, setDigest: release.setDigest, images: release.images, backup: record.backup }),
      deployedAt: at,
      previous: state.current ? { version: state.current.version, setDigest: state.current.setDigest, images: state.current.images } : null,
    },
    history: [
      ...state.history,
      { action: from ? 'deploy' : 'bootstrap', version: release.version, commit: release.commit, setDigest: release.setDigest, images: release.images, from, at, backup: record.backup },
    ],
  }
  await io.writeJSONAtomic(join(root, STATE_FILE), next)
  await io.unlink(join(root, PENDING_FILE))
  return { deployed: release.version, backup: record.backup, from }
}

async function recoverFailedSwitch(io, root, prodEnvPath, state, release, record, reason) {
  const snapshotVersion = snapshotVersionFor(state)
  const snapshotPath = snapshotPathFor(root, snapshotVersion)
  if (!(await io.exists(snapshotPath))) {
    throw new Error(`Traffic switch failed (${reason}) and the env snapshot for ${snapshotVersion} is missing. Manual recovery on the host is required.`)
  }
  await io.writeText(prodEnvPath, await io.readText(snapshotPath), 0o600)
  const up = await io.composeUp()
  if (!up.ok) {
    throw new Error(`Traffic switch failed (${reason}) and the rollback restart also failed: ${up.error}. Manual recovery on the host is required.`)
  }
  const live = await io.liveHealth()
  if (!live.ok) {
    throw new Error(`Traffic switch failed (${reason}); the rollback containers are up but health does not pass: ${live.error}. Manual recovery on the host is required.`)
  }
  // 'pre-bootstrap' is not a real deployed identity: recording it as current
  // would poison every later version comparison, so it stays as current null.
  const restored = snapshotVersion === 'pre-bootstrap' ? null : await findDeployedIdentity(state, snapshotVersion)
  const at = io.now()
  const next = {
    schemaVersion: 1,
    current: restored ? { ...identityOf(restored), deployedAt: at, previous: null } : null,
    history: [...state.history, { action: 'rollback', version: snapshotVersion, from: release.version, at, ...(restored ? identityOf(restored) : {}) }],
  }
  await io.writeJSONAtomic(join(root, STATE_FILE), next)
  await io.unlink(join(root, PENDING_FILE))
  throw new Error(`Traffic switch failed (${reason}); ${snapshotVersion} is serving again and the rollback was recorded.`)
}

async function findDeployedIdentity(state, version) {
  for (let index = state.history.length - 1; index >= 0; index -= 1) {
    const entry = state.history[index]
    if (entry.version === version && ['deploy', 'bootstrap', 'rollback'].includes(entry.action)) return entry
  }
  return null
}

export async function rollbackRelease(io, { root, prodEnvPath, targetVersion }) {
  const state = await readDeployedState(io, root)
  if (!state.current) throw new Error('Nothing is recorded as deployed, so there is nothing to roll back to.')
  const target = targetVersion ?? state.current.previous?.version ?? null
  if (!target) throw new Error('No earlier deployment is recorded. The first deployment of this host has no rollback target.')
  if (target === state.current.version) throw new Error(`${target} is already serving.`)
  const record = await findDeployedIdentity(state, target)
  if (!record) throw new Error(`${target} was never deployed by this tool, so its digests and env snapshot are unknown.`)
  const snapshotPath = snapshotPathFor(root, target)
  if (!(await io.exists(snapshotPath))) throw new Error(`The env snapshot for ${target} is missing; a rollback needs the exact previous env file.`)
  await verifyImages(io, recordedRelease(record, target))
  await io.writeText(prodEnvPath, await io.readText(snapshotPath), 0o600)
  const up = await io.composeUp()
  if (!up.ok) throw new Error(`The rollback restart failed: ${up.error}. The env file for ${target} is in place; run the compose up manually to converge.`)
  const live = await io.liveHealth()
  if (!live.ok || live.version !== target || live.commit !== record.commit) {
    throw new Error(`The rollback restart did not become healthy for ${target}: ${live.error ?? `the live API reports ${live.version ?? 'nothing'}`}.`)
  }
  const at = io.now()
  const next = {
    schemaVersion: 1,
    current: { ...identityOf(record), deployedAt: at, previous: { version: state.current.version, setDigest: state.current.setDigest, images: state.current.images } },
    history: [...state.history, { action: 'rollback', version: target, commit: record.commit, setDigest: record.setDigest, images: record.images, from: state.current.version, at, backup: record.backup }],
  }
  await io.writeJSONAtomic(join(root, STATE_FILE), next)
  return { rolledBack: target, from: state.current.version }
}
