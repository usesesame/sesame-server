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
  SESAME_TRUSTED_PROXIES: '172.30.0.0/24',
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
if (development.services.api?.environment?.SESAME_DEPLOYMENT_PROFILE) {
  throw new Error('The development stack must default to the operator deployment profile.')
}
if (production.services.api?.environment?.SESAME_DEPLOYMENT_PROFILE !== 'project') {
  throw new Error('The production stack must run the project deployment profile.')
}
if (candidate.services['candidate-api']?.environment?.SESAME_DEPLOYMENT_PROFILE !== 'project') {
  throw new Error('The candidate check must run the project deployment profile.')
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
const productionNetwork = production.networks?.default
if (productionNetwork?.name !== 'sesame-prod_default') {
  throw new Error('The production stack must pin the network name the candidate check joins.')
}
const productionSubnets = (productionNetwork?.ipam?.config ?? []).map((entry) => entry.subnet)
if (productionSubnets.length !== 1 || productionSubnets[0] !== productionValues.SESAME_TRUSTED_PROXIES) {
  throw new Error('The production network subnet and SESAME_TRUSTED_PROXIES must be the same range.')
}
if (production.services.api?.environment?.SESAME_TRUSTED_PROXIES !== productionValues.SESAME_TRUSTED_PROXIES) {
  throw new Error('The production API must receive the pinned trusted proxy range.')
}
if ((production.services.api?.extra_hosts ?? []).some((entry) => /mail\.usesesame\.app/.test(entry))) {
  throw new Error('The production stack must not ship a deployment-specific mail host mapping.')
}

const applicationServices = ['migrate', 'api', 'account', 'admin', 'gateway']
for (const service of applicationServices) {
  const definition = production.services[service]
  if (!definition) throw new Error(`Production ${service} is missing from the stack.`)
  if (definition.read_only !== true) throw new Error(`Production ${service} must keep a read-only root filesystem.`)
  if (!definition.tmpfs?.some((entry) => entry === '/tmp' || entry.startsWith('/tmp:'))) {
    throw new Error(`Production ${service} must keep a writable tmpfs at /tmp.`)
  }
  if (!definition.cap_drop?.includes('ALL')) throw new Error(`Production ${service} must keep cap_drop: [ALL].`)
  if (!definition.security_opt?.includes('no-new-privileges:true')) {
    throw new Error(`Production ${service} must keep no-new-privileges:true.`)
  }
  if (!Number.isInteger(Number(definition.pids_limit)) || Number(definition.pids_limit) < 1) {
    throw new Error(`Production ${service} must keep a positive pids limit.`)
  }
  if (!Number.isFinite(Number(definition.mem_limit)) || Number(definition.mem_limit) < 64 * 1024 * 1024) {
    throw new Error(`Production ${service} must keep a memory limit of at least 64 MiB.`)
  }
}
for (const service of ['account', 'admin']) {
  const tmpfs = production.services[service].tmpfs ?? []
  for (const mount of ['/var/cache/nginx', '/run']) {
    if (!tmpfs.some((entry) => entry === mount || entry.startsWith(`${mount}:`))) {
      throw new Error(`Production ${service} must keep a writable tmpfs at ${mount} for nginx.`)
    }
  }
}
if (production.services.db?.read_only === true) throw new Error('PostgreSQL must keep a writable root filesystem.')
if (production.services.db?.cap_drop?.includes('ALL')) {
  throw new Error('PostgreSQL must keep the capabilities its entrypoint needs to prepare the data directory.')
}
if (!production.services.db?.security_opt?.includes('no-new-privileges:true')) {
  throw new Error('PostgreSQL must keep no-new-privileges:true.')
}
if (!Number.isInteger(Number(production.services.db?.pids_limit)) || Number(production.services.db.pids_limit) < 1) {
  throw new Error('PostgreSQL must keep a positive pids limit.')
}
if (!Number.isFinite(Number(production.services.db?.mem_limit)) || Number(production.services.db.mem_limit) < 256 * 1024 * 1024) {
  throw new Error('PostgreSQL must keep a memory limit of at least 256 MiB.')
}
for (const service of ['api', 'account', 'admin', 'gateway']) {
  const ports = production.services[service]?.ports ?? []
  if (ports.length !== 1 || ports[0]?.host_ip !== '127.0.0.1') {
    throw new Error(`Production ${service} must keep its single loopback port binding.`)
  }
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
