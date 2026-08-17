import { spawnSync } from 'node:child_process'
import { existsSync, readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const backendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const repositoryRoot = resolve(backendRoot, '..')
const [action, email] = process.argv.slice(2)
if (!['bootstrap', 'reset'].includes(action)) {
  console.error('Usage: npm run admin:bootstrap -- <bootstrap|reset> admin@example.com')
  process.exit(2)
}
if (!email) {
  console.error('Usage: npm run admin:bootstrap -- <bootstrap|reset> admin@example.com')
  process.exit(2)
}

// Docker Compose interpolates the root .env, so the running API's admin key
// lives there. This script used to read the key only from the shell, which let
// the CLI encrypt an administrator's MFA secret under one key while the API
// decrypted with another. Nothing reports that mismatch: every later sign-in
// just answers "Email, password, or MFA code is incorrect", which reads as the
// account having lost its password. Reading the same file both do removes the
// gap rather than documenting it.
function readEnvFile(path) {
  const values = new Map()
  if (!existsSync(path)) return values
  for (const line of readFileSync(path, 'utf8').split(/\r?\n/)) {
    const match = line.match(/^\s*([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*)$/)
    if (!match) continue
    values.set(match[1], match[2].trim().replace(/^(['"])(.*)\1$/, '$2'))
  }
  return values
}

// A self-hosted deployment keeps its secrets in the file `npm run setup`
// writes; the monorepo still keeps them in the repository-root .env that
// `npm run api:up` writes. Read whichever exists, nearest first.
const fileValues = existsSync(resolve(backendRoot, 'deploy', 'compose', '.env'))
  ? readEnvFile(resolve(backendRoot, 'deploy', 'compose', '.env'))
  : readEnvFile(resolve(repositoryRoot, '.env'))
const adminKey = process.env.SESAME_ADMIN_ENCRYPTION_KEY?.trim() || fileValues.get('SESAME_ADMIN_ENCRYPTION_KEY')
if (!adminKey) {
  console.error(
    'SESAME_ADMIN_ENCRYPTION_KEY is not set and the repository root .env does not define it.\n' +
      'Run `npm run api:up` once to create the local admin secrets.',
  )
  process.exit(2)
}
// An explicit shell value that disagrees with the running API is the exact
// mistake this script exists to prevent, so say so instead of proceeding.
const fileKey = fileValues.get('SESAME_ADMIN_ENCRYPTION_KEY')
if (fileKey && process.env.SESAME_ADMIN_ENCRYPTION_KEY?.trim() && fileKey !== process.env.SESAME_ADMIN_ENCRYPTION_KEY.trim()) {
  console.error(
    'SESAME_ADMIN_ENCRYPTION_KEY in your shell differs from the one in the repository root .env.\n' +
      'Docker Compose starts the API with the .env value, so the shell value would write an MFA secret\n' +
      'the API cannot read. Unset the shell variable, or point DATABASE_URL at the matching deployment.',
  )
  process.exit(2)
}

const environment = {
  ...process.env,
  DATABASE_URL:
    process.env.DATABASE_URL ||
    fileValues.get('DATABASE_URL') ||
    (fileValues.get('SESAME_DATABASE_PASSWORD')
      ? `postgres://sesame:${fileValues.get('SESAME_DATABASE_PASSWORD')}@127.0.0.1:5432/sesame?sslmode=disable`
      : 'postgres://sesame:sesame-development-only@127.0.0.1:5432/sesame?sslmode=disable'),
  SESAME_ADMIN_ORIGIN:
    process.env.SESAME_ADMIN_ORIGIN || fileValues.get('SESAME_ADMIN_ORIGIN') || 'http://localhost:4174',
  SESAME_ADMIN_ENCRYPTION_KEY: adminKey,
}

// A self-hoster should not need a Go toolchain to create the first
// administrator, and the container already has the database on its network and
// the deployment's admin key in its environment. Use it when it is running,
// and fall back to a local `go run` for development in a source checkout.
const compose = ['compose', '-f', 'deploy/compose/compose.yaml']
const stackIsUp = existsSync(resolve(backendRoot, 'deploy', 'compose', 'compose.yaml'))
  && spawnSync('docker', [...compose, 'ps', '--status', 'running', '--quiet', 'api'], {
    cwd: backendRoot, encoding: 'utf8',
  }).stdout?.trim()

const result = stackIsUp
  ? spawnSync('docker', [...compose, 'exec', '-T', '--', 'api', '/sesame-adminctl', action, email], {
    cwd: backendRoot, env: environment, stdio: 'inherit',
  })
  : spawnSync('go', ['run', './cmd/adminctl', action, email], { cwd: backendRoot, env: environment, stdio: 'inherit' })
if (result.error) {
  console.error(`Could not run the admin bootstrap command: ${result.error.message}`)
  process.exit(1)
}
process.exit(result.status ?? 1)
