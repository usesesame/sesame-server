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
const production = check('deploy/compose/compose.prod.yaml', productionValues)
const candidate = check('deploy/compose/compose.candidate-check.yaml', productionValues)

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
digestReference(candidate.services.api?.image, 'Candidate check')
if (candidate.services.api?.build) throw new Error('The candidate check must not contain a build context.')
if (candidate.networks?.default?.name !== 'sesame-prod_default' || candidate.networks?.default?.external !== true) {
  throw new Error('The candidate check must join the production network as an external network.')
}

function check(file, values) {
  const result = spawnSync('docker', ['compose', '--file', file, 'config', '--format', 'json'], {
    encoding: 'utf8',
    env: { ...process.env, ...values },
  })
  if (result.status !== 0) {
    process.stderr.write(result.stderr)
    process.exit(result.status ?? 1)
  }
  return JSON.parse(result.stdout)
}
