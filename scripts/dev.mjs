import { spawn, spawnSync } from 'node:child_process'
import { existsSync, readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { parseEnvText } from './setup-lib.mjs'

const backendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const composeFile = resolve(backendRoot, 'deploy', 'compose', 'compose.yaml')
const devComposeFile = resolve(backendRoot, 'deploy', 'compose', 'compose.dev.yaml')
const envFile = resolve(backendRoot, 'deploy', 'compose', '.env')

if (!existsSync(envFile)) {
  console.error('deploy/compose/.env is missing. Run `npm run setup` first: it creates the secrets the database and API need.')
  process.exit(1)
}

const composeEnvironment = parseEnvText(readFileSync(envFile, 'utf8'))
const fromCompose = (name, fallback) => process.env[name] || composeEnvironment.get(name) || fallback
const databasePassword = fromCompose('SESAME_DATABASE_PASSWORD', 'sesame-development-only')
const localDatabaseUrl = `postgres://sesame:${encodeURIComponent(databasePassword)}@127.0.0.1:5432/sesame?sslmode=disable`

const environment = {
  ...process.env,
  DATABASE_URL: process.env.DATABASE_URL || localDatabaseUrl,
  SESAME_API_ADDR: process.env.SESAME_API_ADDR || '127.0.0.1:8787',
  SESAME_WEB_ORIGIN: process.env.SESAME_WEB_ORIGIN || fromCompose('SESAME_ACCOUNT_ORIGIN', 'http://localhost:4175'),
  SESAME_SESSION_SECURE: process.env.SESAME_SESSION_SECURE || 'false',
  SESAME_ADMIN_ORIGIN: fromCompose('SESAME_ADMIN_ORIGIN', 'http://localhost:4174'),
  SESAME_ADMIN_SESSION_SECURE: process.env.SESAME_ADMIN_SESSION_SECURE || 'false',
  SESAME_CAPABILITY_SIGNING_KEY: fromCompose('SESAME_CAPABILITY_SIGNING_KEY', ''),
  SESAME_ADMIN_ENCRYPTION_KEY: fromCompose('SESAME_ADMIN_ENCRYPTION_KEY', ''),
  SESAME_ADMIN_IP_PEPPER: fromCompose('SESAME_ADMIN_IP_PEPPER', ''),
}

function runCompose(args) {
  const command = spawnSync(
    'docker',
    ['compose', '--file', composeFile, '--file', devComposeFile, '--env-file', envFile, ...args],
    { cwd: backendRoot, stdio: 'inherit' },
  )
  if (command.error) {
    console.error(`Could not run Docker Compose: ${command.error.message}`)
    process.exit(1)
  }
  if (command.status !== 0) process.exit(command.status ?? 1)
}

if (!process.env.DATABASE_URL) {
  console.log('Starting Sesame\'s local PostgreSQL container…')
  // The native Go process owns port 8787 during this command. Stop only the
  // containerized API and keep its PostgreSQL volume intact.
  runCompose(['stop', 'api'])
  runCompose(['up', '-d', '--wait', 'db'])
}

console.log(`Starting Sesame API at http://${environment.SESAME_API_ADDR}`)
const api = spawn('go', ['run', './cmd/api'], {
  cwd: backendRoot,
  env: environment,
  stdio: 'inherit',
})

for (const signal of ['SIGINT', 'SIGTERM']) {
  process.on(signal, () => api.kill(signal))
}

api.on('error', (error) => {
  console.error(`Could not start the Sesame API: ${error.message}`)
  process.exitCode = 1
})

api.on('exit', (code, signal) => {
  if (signal) process.kill(process.pid, signal)
  else process.exit(code ?? 1)
})
