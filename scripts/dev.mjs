import { spawn, spawnSync } from 'node:child_process'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const backendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const repositoryRoot = resolve(backendRoot, '..')
const localDatabaseUrl = 'postgres://sesame:sesame-development-only@127.0.0.1:5432/sesame?sslmode=disable'

const environment = {
  ...process.env,
  DATABASE_URL: process.env.DATABASE_URL || localDatabaseUrl,
  SESAME_API_ADDR: process.env.SESAME_API_ADDR || '127.0.0.1:8787',
  SESAME_API_VERSION: process.env.SESAME_API_VERSION || '0.1.0-dev',
  SESAME_WEB_ORIGIN: process.env.SESAME_WEB_ORIGIN || 'http://localhost:4173',
  SESAME_SESSION_SECURE: process.env.SESAME_SESSION_SECURE || 'false',
  SESAME_ADMIN_ORIGIN: process.env.SESAME_ADMIN_ORIGIN || 'http://localhost:4174',
  SESAME_ADMIN_SESSION_SECURE: process.env.SESAME_ADMIN_SESSION_SECURE || 'false',
  SESAME_ADMIN_ENCRYPTION_KEY: process.env.SESAME_ADMIN_ENCRYPTION_KEY || '',
  SESAME_ADMIN_IP_PEPPER: process.env.SESAME_ADMIN_IP_PEPPER || '',
}

function runCompose(args) {
  const command = spawnSync(
    'docker',
    ['compose', '-f', resolve(repositoryRoot, 'compose.yaml'), ...args],
    { cwd: repositoryRoot, stdio: 'inherit' },
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
