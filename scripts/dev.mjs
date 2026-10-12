import { spawn, spawnSync } from 'node:child_process'
import { existsSync, readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { developmentApiEnvironment, parseEnvText } from './setup-lib.mjs'

const backendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const composeFile = resolve(backendRoot, 'deploy', 'compose', 'compose.yaml')
const devComposeFile = resolve(backendRoot, 'deploy', 'compose', 'compose.dev.yaml')
const envFile = resolve(backendRoot, 'deploy', 'compose', '.env')

if (!existsSync(envFile)) {
  console.error('deploy/compose/.env is missing. Run `npm run setup` first: it creates the secrets the database and API need.')
  process.exit(1)
}

const composeEnvironment = parseEnvText(readFileSync(envFile, 'utf8'))
const environment = developmentApiEnvironment(process.env, composeEnvironment)

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
  runCompose(['exec', '-T', 'db', 'sh', '/docker-entrypoint-initdb.d/10-roles.sh'])
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
