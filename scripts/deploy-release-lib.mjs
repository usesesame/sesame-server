import { createHash } from 'node:crypto'
import { createGunzip } from 'node:zlib'
import { Readable } from 'node:stream'
import { dirname, join } from 'node:path'
import { digestReference, releaseIdentity } from './release-contract.mjs'

const IMAGE_FIELDS = [
  ['api', 'SESAME_API_IMAGE', 'API'],
  ['account', 'SESAME_ACCOUNT_IMAGE', 'Account'],
  ['admin', 'SESAME_ADMIN_IMAGE', 'Admin'],
]

const PENDING_FILE = 'pending.json'
const STATE_FILE = 'deployed.json'
const STAGING_ENV = 'deploy-candidate.env'
const BACKUP_MARKER = '-- PostgreSQL database dump'

export function parseRelease(bytes) {
  const text = Buffer.from(bytes).toString('utf8')
  const raw = JSON.parse(text)
  if (raw?.schemaVersion !== 1) throw new Error('The server release manifest must carry schemaVersion 1.')
  const identity = releaseIdentity(raw.version, raw.commit)
  const images = {}
  for (const [component, , label] of IMAGE_FIELDS) {
    images[component] = digestReference(raw.images?.[component], label)
  }
  return { ...identity, images, setDigest: createHash('sha256').update(text).digest('hex') }
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

export async function assertUsableBackup(gzipBytes) {
  if (!Buffer.isBuffer(gzipBytes) || gzipBytes.length < 1024 || gzipBytes[0] !== 0x1f || gzipBytes[1] !== 0x8b) {
    throw new Error('The pre-deployment backup is not a usable gzip dump.')
  }
  const prefix = await decompressedPrefix(gzipBytes, 64 * 1024)
  if (!prefix.includes(BACKUP_MARKER)) throw new Error('The pre-deployment backup does not contain a PostgreSQL dump.')
  return createHash('sha256').update(gzipBytes).digest('hex')
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

async function verifyImages(io, release) {
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

  if (!resumed) {
    await verifyImages(io, release)
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
    const gzipBytes = await io.takeBackup()
    const sha256 = await assertUsableBackup(gzipBytes)
    const file = join(root, 'backups', `sesame-${release.version}-${stamp(io.now())}.sql.gz`)
    await io.mkdirp(dirname(file))
    await io.writeBinary(file, gzipBytes)
    record.backup = { file, sha256, bytes: gzipBytes.length }
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
  record.phase = 'checked'
  await savePending(io, root, record)

  const liveBefore = await io.liveHealth()
  if (liveBefore.ok && liveBefore.version === release.version && liveBefore.commit === release.commit) {
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
  const restored = await findDeployedIdentity(state, snapshotVersion)
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
