import { spawnSync } from 'node:child_process'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { resolveAgeTools } from './selfhost-export-check.mjs'

const repositoryRoot = dirname(dirname(fileURLToPath(import.meta.url)))
const auditCommand = ['node', '../../scripts/audit-dependencies.mjs']

export const jobEnvironment = {
  account: {
    VITE_SESAME_API_URL: 'https://api.test.invalid',
    VITE_SESAME_SITE_ORIGIN: 'https://website.test.invalid',
  },
  admin: { VITE_SESAME_API_URL: 'https://api.test.invalid' },
}

export const steps = [
  { job: 'server', directory: '.', command: ['npm', 'run', 'ci:checks'] },
  { job: 'server', directory: '.', command: ['npm', 'run', 'test:fuzz'] },
  { job: 'server', directory: '.', command: ['npm', 'run', 'test:race'] },
  { job: 'server', directory: '.', command: ['npm', 'run', 'hosted:smoke'] },
  { job: 'server', directory: '.', command: ['npm', 'run', 'hosted:upgrade'] },
  { job: 'server', directory: '.', command: ['npm', 'run', 'vuln:check'] },
  { job: 'account', directory: 'web/account', command: ['npm', 'ci'] },
  { job: 'account', directory: 'web/account', command: ['npm', 'run', 'ci'] },
  { job: 'account', directory: 'web/account', command: ['npm', 'test'] },
  { job: 'account', directory: 'web/account', command: ['npm', 'run', 'build'] },
  { job: 'account', directory: 'web/account', command: auditCommand },
  { job: 'admin', directory: 'web/admin', command: ['npm', 'ci'] },
  { job: 'admin', directory: 'web/admin', command: ['npm', 'run', 'ci'] },
  { job: 'admin', directory: 'web/admin', command: ['npm', 'test'] },
  { job: 'admin', directory: 'web/admin', command: ['npm', 'run', 'build'] },
  { job: 'admin', directory: 'web/admin', command: auditCommand },
  { job: 'selfhost', directory: 'web/console', command: ['npm', 'ci'] },
  { job: 'selfhost', directory: 'web/console', command: ['npm', 'run', 'release:check'] },
  { job: 'selfhost', directory: 'web/console', command: auditCommand },
  { job: 'selfhost', directory: '.', command: ['npm', 'run', 'backup:contract'], environment: { SESAME_REQUIRE_AGE: '1' } },
  { job: 'selfhost', directory: '.', command: ['npm', 'run', 'selfhost:smoke', '--', '--skip-console-build'] },
  { job: 'selfhost', directory: '.', command: ['npm', 'run', 'backup:contract:age'] },
]

function invocation([program, ...args]) {
  if (program === 'node') return { file: process.execPath, args }
  if (program === 'npm' && process.env.npm_execpath) return { file: process.execPath, args: [process.env.npm_execpath, ...args] }
  throw new Error(`Run this through npm run verify so ${program} resolves to the npm that started it.`)
}

function runStep(step) {
  const { file, args } = invocation(step.command)
  console.log(`\n[${step.job}] ${step.directory === '.' ? '' : `${step.directory}: `}${step.command.join(' ')}`)
  const result = spawnSync(file, args, {
    cwd: resolve(repositoryRoot, step.directory),
    env: { ...process.env, CI: 'true', ...jobEnvironment[step.job], ...step.environment },
    stdio: 'inherit',
  })
  if (result.error) throw result.error
  if (result.status !== 0) throw new Error(`${step.command.join(' ')} exited ${result.status ?? result.signal}`)
}

function verify() {
  if (!process.env.SESAME_TEST_DATABASE_URL) {
    throw new Error('Set SESAME_TEST_DATABASE_URL to a throwaway PostgreSQL database first, because the pull request gate runs the database tests.')
  }
  resolveAgeTools()
  for (const step of steps) runStep(step)
  console.log(`\nAll ${steps.length} steps passed. The image and Compose jobs in .github/workflows/ci.yml need Docker and did not run.`)
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    verify()
  } catch (error) {
    console.error(error.message)
    process.exit(1)
  }
}
