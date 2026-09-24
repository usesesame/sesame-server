import { spawnSync } from 'node:child_process'
import { digestReference } from './release-contract.mjs'

const digest = `sha256:${'0'.repeat(64)}`
const common = {
  SESAME_DATABASE_PASSWORD: 'sesame-config-only',
  SESAME_CAPABILITY_SIGNING_KEY: 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA',
  SESAME_CAPABILITY_PUBLIC_KEY: 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA',
  SESAME_ADMIN_ENCRYPTION_KEY: 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA',
  SESAME_ADMIN_IP_PEPPER: 'sesame-config-only',
  SESAME_ACCOUNT_ORIGIN: 'https://account.test.invalid',
  SESAME_ADMIN_ORIGIN: 'https://admin.test.invalid',
  SESAME_PUBLIC_SITE_ORIGIN: 'https://website.test.invalid',
  SESAME_API_ORIGIN: 'https://api.test.invalid',
  SESAME_SITE_ORIGIN: 'https://website.test.invalid',
  SESAME_REGISTRATION_MODE: 'closed',
  SESAME_RP_ID: 'account.test.invalid',
  SESAME_TRUSTED_PROXIES: '172.16.0.0/12',
  SESAME_SERVER_CONTEXT: '../..',
}

const productionValues = {
  ...common,
  SESAME_API_IMAGE: `registry.test.invalid/sesame-api@${digest}`,
  SESAME_ACCOUNT_IMAGE: `registry.test.invalid/sesame-account@${digest}`,
  SESAME_ADMIN_IMAGE: `registry.test.invalid/sesame-admin@${digest}`,
}
const development = check('deploy/compose/compose.yaml', common)
const developmentWithOverride = check(['deploy/compose/compose.yaml', 'deploy/compose/compose.dev.yaml'], common)
const production = check('deploy/compose/compose.prod.yaml', productionValues)
const candidate = check('deploy/compose/compose.candidate-check.yaml', productionValues)

// `npm run dev` runs the API natively, so the override must publish the database.
const developmentDatabasePorts = developmentWithOverride.services.db?.ports ?? []
if (developmentDatabasePorts.length !== 1 || developmentDatabasePorts[0]?.target !== 5432 || developmentDatabasePorts[0]?.host_ip !== '127.0.0.1') {
  throw new Error('The development override must publish PostgreSQL on 127.0.0.1:5432 for npm run dev.')
}

for (const service of ['api', 'migrate', 'account', 'admin']) {
  if (!development.services[service]?.build) throw new Error(`Development ${service} must remain locally buildable.`)
  if (production.services[service]?.build) throw new Error(`Production ${service} must not contain a build context.`)
  digestReference(production.services[service]?.image, `Production ${service}`)
}
if (production.services.api.image !== production.services.migrate.image) {
  throw new Error('API and migration jobs must use the same immutable image.')
}
for (const service of ['api', 'account', 'admin']) {
  if (!production.services[service]?.healthcheck) throw new Error(`Production ${service} must define a health check.`)
}
digestReference(candidate.services['candidate-api']?.image, 'Candidate check')
if (candidate.services['candidate-api']?.build) throw new Error('The candidate check must not contain a build context.')
if (candidate.networks?.default?.name !== 'sesame-prod_default' || candidate.networks?.default?.external !== true) {
  throw new Error('The candidate check must join the production network as an external network.')
}

function check(files, values) {
  const fileArgs = (Array.isArray(files) ? files : [files]).flatMap((file) => ['--file', file])
  const result = spawnSync('docker', ['compose', ...fileArgs, 'config', '--format', 'json'], {
    encoding: 'utf8',
    env: { ...process.env, ...values },
  })
  if (result.status !== 0) {
    process.stderr.write(result.stderr)
    process.exit(result.status ?? 1)
  }
  return JSON.parse(result.stdout)
}
