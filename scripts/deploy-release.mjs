import { spawn, spawnSync } from 'node:child_process'
import { createHash, randomBytes } from 'node:crypto'
import { createWriteStream } from 'node:fs'
import { readFile } from 'node:fs/promises'
import { Transform } from 'node:stream'
import { pipeline } from 'node:stream/promises'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { fileIO } from './deploy-io.mjs'
import { BACKUP_COMPLETE_MARKER, classifyDeployment, deployRelease, parseBackupRecipients, parseRelease, readDeployedState, rehearsalEnvFile, rollbackRelease, writeEncryptedBackup } from './deploy-release-lib.mjs'
import { parseEnvText } from './setup-lib.mjs'

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
  ...fileIO,
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
  takeBackup: (destination, recipients) => takeStreamingBackup(destination, recipients),
  rehearse: ({ candidateRef, previousRef }) => rehearseMigrations({ candidateRef, previousRef }),
  backupRecipients: async () => parseBackupRecipients(parseEnvText(await readFile(prodEnvPath, 'utf8')).get('SESAME_BACKUP_AGE_RECIPIENTS')),
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

function spawnDump() {
  return spawn('docker', ['compose', '--file', prodCompose, '--env-file', prodEnvPath, 'exec', '-T', 'db', 'pg_dump', '-U', 'sesame', 'sesame'], { stdio: ['ignore', 'pipe', 'pipe'] })
}

async function takeStreamingBackup(destination, recipients) {
  const child = spawnDump()
  const outcome = childOutcome(child)
  try {
    return await writeEncryptedBackup(child.stdout, destination, {
      recipients,
      encrypt: (source, staging) => encryptBackupWithAge(source, staging, recipients),
      confirmSource: async () => {
        const code = await outcome.exited
        if (outcome.error || code !== 0) throw new Error(`The pre-deployment pg_dump failed${outcome.stderr ? `: ${lastLine(outcome.stderr)}` : '.'}`)
      },
    })
  } catch (error) {
    child.kill()
    throw error
  }
}

async function encryptBackupWithAge(source, destination, recipients) {
  const child = spawn('age', recipients.flatMap((recipient) => ['-r', recipient]), { stdio: ['pipe', 'pipe', 'pipe'] })
  const outcome = childOutcome(child)
  const digest = createHash('sha256')
  let bytes = 0
  const meter = new Transform({
    transform(chunk, encoding, callback) {
      digest.update(chunk)
      bytes += chunk.length
      callback(null, chunk)
    },
  })
  const feed = pipeline(source, child.stdin)
  const collect = pipeline(child.stdout, meter, createWriteStream(destination, { flags: 'wx', mode: 0o600 }))
  const [feedResult, collectResult, exitResult] = await Promise.allSettled([feed, collect, outcome.exited])
  if (outcome.error) throw outcome.error
  if (collectResult.status === 'rejected') throw collectResult.reason
  if (exitResult.status === 'fulfilled' && exitResult.value !== 0) {
    throw new Error(`age exited with status ${exitResult.value}${outcome.stderr ? `: ${lastLine(outcome.stderr)}` : '.'}`)
  }
  if (feedResult.status === 'rejected') throw feedResult.reason
  return { sha256: digest.digest('hex'), bytes }
}

function childOutcome(child) {
  const outcome = { error: null, stderr: '', exited: null }
  child.on('error', (error) => { outcome.error = error })
  child.stderr.on('data', (chunk) => { outcome.stderr = `${outcome.stderr}${chunk}`.slice(-8192) })
  outcome.exited = new Promise((resolveExit) => child.on('close', (code) => resolveExit(code)))
  return outcome
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
  let lastError = null
  for (let attempt = 0; attempt < 5; attempt += 1) {
    let response
    try {
      response = await fetch(url, { signal: AbortSignal.timeout(5000) })
    } catch (error) {
      // A switch recreates containers, so a pooled socket or a rebound port can
      // refuse one connection. Retry connection errors; status codes are real.
      lastError = new Error(`${url} was unreachable: ${error instanceof Error ? error.message : String(error)}`)
      await new Promise((resolveDelay) => setTimeout(resolveDelay, 1000))
      continue
    }
    if (!response.ok) throw new Error(`${url} returned ${response.status}`)
    return response.json()
  }
  throw lastError
}

// The raw env file lacks what compose injects, such as SESAME_WEB_ORIGIN.
async function composeApiEnvironment() {
  const result = spawnSync('docker', ['compose', '--file', prodCompose, '--env-file', prodEnvPath, 'config', '--format', 'json'], { encoding: 'utf8', maxBuffer: 16 * 1024 * 1024 })
  if (result.status !== 0) return null
  const model = JSON.parse(result.stdout)
  return model?.services?.api?.environment ?? null
}

async function rehearseMigrations({ candidateRef, previousRef }) {
  const password = randomBytes(24).toString('base64url')
  const scratchURL = `postgres://sesame:${encodeURIComponent(password)}@127.0.0.1:5432/sesame?sslmode=disable`
  try {
    docker(['run', '-d', '--name', scratchDatabase, '-e', 'POSTGRES_USER=sesame', '-e', `POSTGRES_PASSWORD=${password}`, '-e', 'POSTGRES_DB=sesame', '--tmpfs', '/var/lib/postgresql', scratchPostgresImage])
    // The image answers pg_isready from its temporary init server; require two
    // passes or the restore can land before the real server listens.
    await waitFor(() => {
      if (spawnSync('docker', ['exec', scratchDatabase, 'pg_isready', '-q', '-U', 'sesame', '-d', 'sesame']).status !== 0) return false
      spawnSync('sleep', ['1'])
      return spawnSync('docker', ['exec', scratchDatabase, 'pg_isready', '-q', '-U', 'sesame', '-d', 'sesame']).status === 0
    }, 'the rehearsal database never became ready', 90, 1000)
    await restoreFreshDump(scratchDatabase)
    const migrate = spawnSync('docker', ['run', '--rm', '--entrypoint', '/sesame-migrate', '--network', `container:${scratchDatabase}`, '-e', `DATABASE_URL=${scratchURL}`, candidateRef], { encoding: 'utf8', stdio: ['ignore', 'ignore', 'pipe'] })
    if (migrate.status !== 0) return { ok: false, error: `the candidate migration failed on restored data: ${lastLine(migrate.stderr)}` }
    if (previousRef) {
      // The resolved API environment carries every production secret. Passing it
      // through argv would expose it in the host process list, so it goes through
      // a mode 0600 env file that is removed after the rehearsal.
      const apiEnvironment = await composeApiEnvironment()
      const envFilePath = apiEnvironment ? join(stateRoot, `rehearsal-api-${process.pid}.env`) : null
      try {
        const previousArgs = envFilePath
          ? ['--env-file', envFilePath]
          : ['--env-file', prodEnvPath, '-e', `DATABASE_URL=${scratchURL}`]
        if (envFilePath) await io.writeText(envFilePath, rehearsalEnvFile(apiEnvironment, scratchURL), 0o600)
        docker(['run', '-d', '--name', scratchPrevious, '--network', `container:${scratchDatabase}`, ...previousArgs, previousRef])
        await waitFor(() => spawnSync('docker', ['exec', scratchPrevious, '/sesame-healthcheck']).status === 0, 'the previous revision never became healthy against the migrated schema')
      } finally {
        if (envFilePath) await io.unlink(envFilePath)
      }
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

async function restoreFreshDump(container) {
  const dump = spawnDump()
  const psql = spawn('docker', ['exec', '-i', container, 'psql', '-q', '-v', 'ON_ERROR_STOP=1', '-U', 'sesame', '-d', 'sesame'], { stdio: ['pipe', 'ignore', 'pipe'] })
  const dumpOutcome = childOutcome(dump)
  const psqlOutcome = childOutcome(psql)
  const tail = { value: Buffer.alloc(0) }
  const collector = new Transform({
    transform(chunk, encoding, callback) {
      tail.value = Buffer.concat([tail.value, chunk]).subarray(-4096)
      callback(null, chunk)
    },
  })
  const [streamResult, dumpExit, psqlExit] = await Promise.allSettled([
    pipeline(dump.stdout, collector, psql.stdin),
    dumpOutcome.exited,
    psqlOutcome.exited,
  ])
  if (dumpOutcome.error) throw dumpOutcome.error
  if (psqlOutcome.error) throw psqlOutcome.error
  if (dumpExit.value !== 0) throw new Error(`streaming a rehearsal copy of the database failed: ${lastLine(dumpOutcome.stderr)}`)
  if (psqlExit.value !== 0) throw new Error(`restoring the rehearsal copy failed: ${lastLine(psqlOutcome.stderr)}`)
  if (streamResult.status === 'rejected') throw streamResult.reason
  if (!tail.value.includes(BACKUP_COMPLETE_MARKER)) throw new Error('the rehearsal copy of the database ended before pg_dump finished')
}

function docker(args) {
  const result = spawnSync('docker', args, { encoding: 'utf8', stdio: ['ignore', 'ignore', 'pipe'] })
  if (result.status !== 0) throw new Error(`docker ${args[0]} failed: ${lastLine(result.stderr)}`)
}

function lastLine(text) {
  const lines = String(text ?? '').trim().split('\n')
  return lines[lines.length - 1] || 'no diagnostics'
}
