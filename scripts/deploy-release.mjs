import { spawnSync } from 'node:child_process'
import { randomBytes } from 'node:crypto'
import { gzipSync } from 'node:zlib'
import { chmod, mkdir, readFile, rename, rm, stat, unlink, writeFile } from 'node:fs/promises'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { classifyDeployment, deployRelease, parseRelease, readDeployedState, rollbackRelease } from './deploy-release-lib.mjs'

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const stateRoot = join(repoRoot, 'deploy', 'state')
const prodCompose = join(repoRoot, 'deploy', 'compose', 'compose.prod.yaml')
const candidateCompose = join(repoRoot, 'deploy', 'compose', 'compose.candidate-check.yaml')
const prodEnvPath = join(repoRoot, 'deploy', 'compose', '.env.production')
const scratchDatabase = 'sesame-deploy-rehearsal-db'
const scratchPrevious = 'sesame-deploy-rehearsal-prev'
const scratchPostgresImage = 'postgres:18-alpine@sha256:d3e1620b530c944afa6e887d22eb899824da68e19c52024bf98f5220c88a65b2'
const candidatePort = 8788

const io = {
  readText: (path) => readFile(path, 'utf8'),
  writeText: async (path, text, mode) => {
    await mkdir(dirname(path), { recursive: true })
    await writeFile(path, text, mode === undefined ? {} : { mode })
    if (mode !== undefined) await chmod(path, mode)
  },
  writeBinary: async (path, bytes) => {
    await mkdir(dirname(path), { recursive: true })
    await writeFile(path, bytes)
  },
  writeJSONAtomic: async (path, value, mode) => {
    const staging = `${path}.staging-${process.pid}`
    await mkdir(dirname(path), { recursive: true })
    await writeFile(staging, `${JSON.stringify(value, null, 2)}\n`, { mode: mode ?? 0o600 })
    await rename(staging, path)
  },
  renamePath: (from, to) => rename(from, to),
  exists: async (path) => { try { await stat(path); return true } catch { return false } },
  mkdirp: (path) => mkdir(path, { recursive: true }),
  unlink: async (path) => { try { await unlink(path) } catch (error) { if (error.code !== 'ENOENT') throw error } },
  pullImage: (reference) => {
    const result = spawnSync('docker', ['pull', reference], { encoding: 'utf8', stdio: ['ignore', 'ignore', 'pipe'] })
    if (result.status !== 0) throw new Error(`Could not pull ${reference}: ${lastLine(result.stderr)}`)
  },
  inspectImage: (reference) => {
    const result = spawnSync('docker', ['image', 'inspect', '--format', '{{json .}}', reference], { encoding: 'utf8', maxBuffer: 16 * 1024 * 1024 })
    if (result.status !== 0) return null
    const image = JSON.parse(result.stdout)
    return { repoDigests: image.RepoDigests ?? [], labels: image.Config?.Labels ?? {} }
  },
  takeBackup: () => {
    const result = spawnSync('docker', ['compose', '--file', prodCompose, '--env-file', prodEnvPath, 'exec', '-T', 'db', 'pg_dump', '-U', 'sesame', 'sesame'], { encoding: 'buffer', maxBuffer: 1024 * 1024 * 1024 })
    if (result.status !== 0 || !result.stdout?.length) throw new Error(`The pre-deployment pg_dump failed${result.stderr ? `: ${lastLine(result.stderr.toString('utf8'))}` : '.'}`)
    return gzipSync(result.stdout)
  },
  rehearse: ({ backupFile, candidateRef, previousRef }) => rehearseMigrations({ backupFile, candidateRef, previousRef }),
  runMigrations: (stagingEnvPath) => {
    const result = spawnSync('docker', ['compose', '--file', prodCompose, '--env-file', stagingEnvPath, 'run', '--rm', 'migrate'], { encoding: 'utf8', stdio: ['ignore', 'ignore', 'pipe'] })
    if (result.status !== 0) return { ok: false, error: lastLine(result.stderr) }
    return { ok: true }
  },
  candidateHealth: (stagingEnvPath) => verifyCandidate(stagingEnvPath),
  switchTraffic: async (stagingEnvPath) => {
    await io.renamePath(stagingEnvPath, prodEnvPath)
    return composeUp()
  },
  composeUp: () => composeUp(),
  liveHealth: () => probeLiveEndpoints(),
  now: () => new Date().toISOString(),
}

let waitTimeout = 300

const parseWaitTimeout = () => {
  const index = process.argv.indexOf('--wait-timeout')
  const value = index >= 0 ? Number(process.argv[index + 1]) : 300
  if (!Number.isInteger(value) || value < 30 || value > 1800) throw new Error('--wait-timeout must be an integer number of seconds between 30 and 1800.')
  return value
}

const waitTimeoutSeconds = () => waitTimeout

main(process.argv.slice(2)).catch((error) => {
  process.stderr.write(`${error instanceof Error ? error.message : error}\n`)
  process.exitCode = 1
})

async function main(args) {
  waitTimeout = parseWaitTimeout()
  const positional = []
  for (let index = 0; index < args.length; index += 1) {
    if (args[index] === '--wait-timeout') { index += 1; continue }
    positional.push(args[index])
  }
  const [selected, ...rest] = positional
  if (selected === 'status' && rest.length === 0) return status()
  if (selected === 'plan' && rest.length === 1) return plan(rest[0])
  if (selected === 'deploy' && rest.length === 1) {
    const release = parseRelease(await readFile(resolve(rest[0])))
    const result = await deployRelease(io, { root: stateRoot, prodEnvPath, release })
    process.stdout.write(`Deployed ${result.deployed}. Backup: ${result.backup.file} (sha256 ${result.backup.sha256}).\n`)
    return
  }
  if (selected === 'rollback' && rest.length <= 1) {
    const result = await rollbackRelease(io, { root: stateRoot, prodEnvPath, targetVersion: rest[0] ?? undefined })
    process.stdout.write(`Rolled back to ${result.rolledBack} from ${result.from}.\n`)
    return
  }
  throw new Error('Usage: deploy-release.mjs <plan|deploy|rollback|status> [server-release.json] [version] [--wait-timeout seconds]')
}

async function plan(releasePath) {
  const release = parseRelease(await readFile(resolve(releasePath)))
  const state = await readDeployedState(io, stateRoot)
  const classification = classifyDeployment(state, release)
  process.stdout.write(`${JSON.stringify({ action: classification.action, current: state.current?.version ?? null, candidate: release.version, setDigest: release.setDigest, images: Object.fromEntries(Object.entries(release.images).map(([component, value]) => [component, value.reference])) }, null, 2)}\n`)
  return classification
}

async function status() {
  const state = await readDeployedState(io, stateRoot)
  const summary = (entry) => ({ version: entry.version, commit: entry.commit, at: entry.deployedAt ?? entry.at, backup: entry.backup?.file ?? null })
  process.stdout.write(`${JSON.stringify({ current: state.current ? summary(state.current) : null, history: state.history.slice(-10).map((entry) => ({ action: entry.action, version: entry.version, from: entry.from, at: entry.at })) }, null, 2)}\n`)
}

function composeUp() {
  const result = spawnSync('docker', ['compose', '--file', prodCompose, '--env-file', prodEnvPath, 'up', '-d', '--wait', '--wait-timeout', String(waitTimeoutSeconds())], { encoding: 'utf8', stdio: ['ignore', 'ignore', 'pipe'] })
  if (result.status !== 0) return { ok: false, error: lastLine(result.stderr) }
  return { ok: true }
}

async function verifyCandidate(stagingEnvPath) {
  try {
    const up = spawnSync('docker', ['compose', '--file', candidateCompose, '--env-file', stagingEnvPath, 'up', '-d', '--wait', '--wait-timeout', '120'], { encoding: 'utf8', stdio: ['ignore', 'ignore', 'pipe'] })
    if (up.status !== 0) return { ok: false, error: lastLine(up.stderr) }
    const livez = await probeJSON(`http://127.0.0.1:${candidatePort}/livez`)
    const readyz = await fetch(`http://127.0.0.1:${candidatePort}/readyz`, { signal: AbortSignal.timeout(5000) })
    if (!readyz.ok) return { ok: false, error: `the candidate readiness endpoint returned ${readyz.status}` }
    return { ok: true, version: livez.version, commit: livez.commit }
  } catch (error) {
    return { ok: false, error: error instanceof Error ? error.message : String(error) }
  } finally {
    spawnSync('docker', ['compose', '--file', candidateCompose, '--env-file', stagingEnvPath, 'down', '--volumes', '--remove-orphans'], { stdio: 'ignore' })
  }
}

async function probeLiveEndpoints() {
  try {
    const livez = await probeJSON('http://127.0.0.1:8787/livez')
    const readyz = await fetch('http://127.0.0.1:8787/readyz', { signal: AbortSignal.timeout(5000) })
    if (!readyz.ok) return { ok: false, error: `the readiness endpoint returned ${readyz.status}` }
    for (const [component, port] of [['account', 4175], ['admin', 4174]]) {
      const release = await probeJSON(`http://127.0.0.1:${port}/release.json`)
      if (release.component !== component || release.version !== livez.version || release.commit !== livez.commit) {
        return { ok: false, error: `the ${component} portal does not report release ${livez.version} at ${livez.commit}` }
      }
    }
    return { ok: true, version: livez.version, commit: livez.commit }
  } catch (error) {
    return { ok: false, error: error instanceof Error ? error.message : String(error) }
  }
}

async function probeJSON(url) {
  const response = await fetch(url, { signal: AbortSignal.timeout(5000) })
  if (!response.ok) throw new Error(`${url} returned ${response.status}`)
  return response.json()
}

async function rehearseMigrations({ backupFile, candidateRef, previousRef }) {
  const password = randomBytes(24).toString('base64url')
  const scratchURL = `postgres://sesame:${encodeURIComponent(password)}@127.0.0.1:5432/sesame?sslmode=disable`
  try {
    docker(['run', '-d', '--name', scratchDatabase, '-e', 'POSTGRES_USER=sesame', '-e', `POSTGRES_PASSWORD=${password}`, '-e', 'POSTGRES_DB=sesame', '--tmpfs', '/var/lib/postgresql', scratchPostgresImage])
    await waitFor(() => spawnSync('docker', ['exec', scratchDatabase, 'pg_isready', '-q', '-U', 'sesame', '-d', 'sesame']).status === 0, 'the rehearsal database never became ready')
    const restore = spawnSync('sh', ['-c', `umask 077; gunzip -c "$1" > /tmp/sesame-restore.$$.sql || { rm -f /tmp/sesame-restore.$$.sql; exit 1; }; docker exec -i ${scratchDatabase} psql -q -v ON_ERROR_STOP=1 -U sesame -d sesame < /tmp/sesame-restore.$$.sql; status=$?; rm -f /tmp/sesame-restore.$$.sql; exit $status`, 'sh', backupFile], { encoding: 'utf8', stdio: ['ignore', 'ignore', 'pipe'], maxBuffer: 64 * 1024 * 1024 })
    if (restore.status !== 0) return { ok: false, error: `restoring the backup into the rehearsal database failed: ${lastLine(restore.stderr)}` }
    const migrate = spawnSync('docker', ['run', '--rm', '--network', `container:${scratchDatabase}`, '-e', `DATABASE_URL=${scratchURL}`, candidateRef, '/sesame-migrate'], { encoding: 'utf8', stdio: ['ignore', 'ignore', 'pipe'] })
    if (migrate.status !== 0) return { ok: false, error: `the candidate migration failed on restored data: ${lastLine(migrate.stderr)}` }
    if (previousRef) {
      docker(['run', '-d', '--name', scratchPrevious, '--network', `container:${scratchDatabase}`, '--env-file', prodEnvPath, '-e', `DATABASE_URL=${scratchURL}`, previousRef])
      await waitFor(() => spawnSync('docker', ['exec', scratchPrevious, '/sesame-healthcheck']).status === 0, 'the previous revision never became healthy against the migrated schema')
    }
    return { ok: true }
  } catch (error) {
    return { ok: false, error: error instanceof Error ? error.message : String(error) }
  } finally {
    spawnSync('docker', ['rm', '-f', scratchDatabase, scratchPrevious], { stdio: 'ignore' })
  }
}

async function waitFor(condition, message, attempts = 60, delayMs = 1000) {
  for (let index = 0; index < attempts; index += 1) {
    if (condition()) return
    await new Promise((resolveDelay) => setTimeout(resolveDelay, delayMs))
  }
  throw new Error(`${message} within the rehearsal time budget.`)
}

function docker(args) {
  const result = spawnSync('docker', args, { encoding: 'utf8', stdio: ['ignore', 'ignore', 'pipe'] })
  if (result.status !== 0) throw new Error(`docker ${args[0]} failed: ${lastLine(result.stderr)}`)
}

function lastLine(text) {
  const lines = String(text ?? '').trim().split('\n')
  return lines[lines.length - 1] || 'no diagnostics'
}
