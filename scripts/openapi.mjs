import assert from 'node:assert/strict'
import { mkdirSync, readFileSync, readdirSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const backendRoot = dirname(dirname(fileURLToPath(import.meta.url)))
const httpRoot = join(backendRoot, 'internal', 'httpapi')
const outputPath = join(backendRoot, 'openapi', 'openapi.json')
const registrationFiles = ['server.go', 'admin_auth.go', 'sync_routes.go']
const goSources = readdirSync(httpRoot)
  .filter((name) => name.endsWith('.go') && !name.endsWith('_test.go'))
  .map((name) => [name, readFileSync(join(httpRoot, name), 'utf8')])

const dynamicRoutes = {
  '/v1/account/sessions/': [
    ['/v1/account/sessions/{sessionId}', ['DELETE']],
  ],
  '/v1/downloads/': [
    ['/v1/downloads/{ticket}', ['GET']],
  ],
  '/v1/account/support/': [
    ['/v1/account/support/{ticketId}', ['GET']],
    ['/v1/account/support/{ticketId}/reply', ['POST']],
    ['/v1/account/support/{ticketId}/close', ['POST']],
    ['/v1/account/support/{ticketId}/reopen', ['POST']],
  ],
  '/v1/account/devices/': [
    ['/v1/account/devices/{deviceId}', ['DELETE', 'PATCH']],
  ],
  '/v1/desktop/update-tickets/': [
    ['/v1/desktop/update-tickets/{ticket}', ['GET']],
  ],
  '/v1/admin/users/': [
    ['/v1/admin/users/{accountId}', ['GET', 'DELETE']],
    ['/v1/admin/users/{accountId}/owner-release', ['POST', 'DELETE']],
    ['/v1/admin/users/{accountId}/beta', ['POST', 'DELETE']],
    ['/v1/admin/users/{accountId}/suspend', ['POST', 'DELETE']],
    ['/v1/admin/users/{accountId}/sessions', ['DELETE']],
    ['/v1/admin/users/{accountId}/devices/{deviceId}', ['DELETE']],
  ],
  '/v1/admin/flags/': [
    ['/v1/admin/flags/{flag}', ['PATCH']],
  ],
  '/v1/admin/releases/': [
    ['/v1/admin/releases/{platform}', ['PUT']],
  ],
  '/v1/admin/plans/': [
    ['/v1/admin/plans/{planId}', ['PATCH']],
  ],
  '/v1/admin/admins/': [
    ['/v1/admin/admins/{adminId}', ['DELETE', 'PATCH']],
  ],
  '/v1/admin/support/': [
    ['/v1/admin/support/{ticketId}', ['GET']],
    ['/v1/admin/support/{ticketId}/reply', ['POST']],
    ['/v1/admin/support/{ticketId}/notes', ['POST']],
    ['/v1/admin/support/{ticketId}/assign', ['POST']],
    ['/v1/admin/support/{ticketId}/status', ['POST']],
  ],
  '/v1/sync/devices/': [
    ['/v1/sync/devices/{deviceId}', ['DELETE']],
    ['/v1/sync/devices/{deviceId}/approve', ['POST']],
    ['/v1/sync/devices/{deviceId}/deny', ['POST']],
    ['/v1/sync/devices/{deviceId}/rekey', ['POST']],
  ],
}

function registeredHandlers() {
  const routes = []
  const pattern = /mux[.]HandleFunc[(]"([^"]+)",[ \t]*(?:service|a)[.]([A-Za-z0-9_]+)[)]/g
  for (const name of registrationFiles) {
    const source = readFileSync(join(httpRoot, name), 'utf8')
    for (const match of source.matchAll(pattern)) {
      routes.push({ registration: match[1], handler: match[2] })
    }
  }
  return routes
}

function handlerMethods(handler) {
  const marker = `func (a *api) ${handler}(`
  for (const [, source] of goSources) {
    const start = source.indexOf(marker)
    if (start < 0) continue
    const tail = source.slice(start)
    const next = tail.slice(5).search(/\nfunc /)
    const body = next < 0 ? tail : tail.slice(0, next + 5)
    return [...new Set(
      [...body.matchAll(/http[.]Method([A-Z][A-Za-z]+)/g)]
        .map((match) => match[1].toUpperCase()),
    )]
  }
  throw new Error(`OpenAPI generator could not find handler ${handler}.`)
}

function tagFor(path) {
  if (!path.startsWith('/v1/')) return 'health'
  if (path.startsWith('/v1/admin/')) return 'admin'
  if (path.startsWith('/v1/sync/')) return 'sync-disabled'
  if (path.startsWith('/v1/account/') || path.startsWith('/v1/auth/') || path.startsWith('/v1/downloads/')) return 'account'
  if (path.startsWith('/v1/desktop/')) return 'desktop'
  if (path === '/v1/release-candidates') return 'release-pipeline'
  return 'public'
}

function isMutation(method) {
  return !['GET', 'HEAD', 'OPTIONS'].includes(method)
}

function authFor(path, method) {
  if (path.startsWith('/v1/admin/')) {
    if (path === '/v1/admin/auth/csrf') return ['public', []]
    if (path === '/v1/admin/auth/login' || path.startsWith('/v1/admin/auth/setup/')) {
      return ['admin-csrf', [{ adminCsrfCookie: [], adminCsrfHeader: [] }]]
    }
    if (path === '/v1/admin/auth/me') return ['admin-session', [{ adminSession: [] }]]
    return isMutation(method)
      ? ['admin-session-csrf', [{ adminSession: [], adminCsrfCookie: [], adminCsrfHeader: [] }]]
      : ['admin-session', [{ adminSession: [] }]]
  }
  if (path.startsWith('/v1/sync/')) return ['desktop-token-built-disabled', [{ desktopToken: [] }]]
  if (path === '/v1/release-candidates') return ['release-pipeline-token', [{ releasePipelineToken: [] }]]
  if (path.startsWith('/v1/desktop/')) {
    if (path === '/v1/desktop/link') return ['single-use-link-code', []]
    return ['desktop-token', [{ desktopToken: [] }]]
  }
  if (path.startsWith('/v1/account/') || path.startsWith('/v1/downloads/')) {
    return isMutation(method)
      ? ['browser-session-csrf', [{ browserSession: [], browserCsrfCookie: [], browserCsrfHeader: [] }]]
      : ['browser-session', [{ browserSession: [] }]]
  }
  if (path.startsWith('/v1/auth/')) {
    if (path === '/v1/auth/csrf' || path === '/v1/auth/registration') return ['public', []]
    if (path === '/v1/auth/me') return ['browser-session', [{ browserSession: [] }]]
    if (path === '/v1/auth/logout' || path === '/v1/auth/email/verification/request') {
      return ['browser-session-csrf', [{ browserSession: [], browserCsrfCookie: [], browserCsrfHeader: [] }]]
    }
    return ['browser-csrf', [{ browserCsrfCookie: [], browserCsrfHeader: [] }]]
  }
  if (path === '/v1/support/requests') return ['browser-csrf', [{ browserCsrfCookie: [], browserCsrfHeader: [] }]]
  return ['public', []]
}

function words(value) {
  return value.replace(/([a-z0-9])([A-Z])/g, '$1 $2').replaceAll('-', ' ').toLowerCase()
}

function operationId(method, path) {
  const pieces = path.match(/[A-Za-z0-9]+/g) ?? []
  return method.toLowerCase() + pieces.map((piece) => piece[0].toUpperCase() + piece.slice(1)).join('')
}

function availability(path) {
  if (path.startsWith('/v1/sync/')) return 'built-disabled'
  if (path.startsWith('/v1/admin/')) return 'configured-admin-only'
  if (path.includes('/passkey/')) return 'configured-webauthn-only'
  return 'shipped'
}

function parameterName(segment) {
  if (segment === 'ticket') return 'Opaque ticket value. Never log or persist it in plaintext.'
  return `Opaque ${words(segment)}.`
}

function build() {
  const registrations = registeredHandlers().filter((route) => route.registration !== '/')
  const covered = new Set()
  const paths = {}
  const operationIds = new Set()

  for (const route of registrations) {
    const expanded = dynamicRoutes[route.registration]
      ?? [[route.registration, handlerMethods(route.handler)]]
    assert.ok(expanded.length > 0, `${route.registration} has no OpenAPI expansion`)
    covered.add(route.registration)
    for (const [path, methods] of expanded) {
      paths[path] ??= {}
      for (const method of methods) {
        const id = operationId(method, path)
        assert.ok(!operationIds.has(id), `duplicate OpenAPI operationId ${id}`)
        assert.ok(!paths[path][method.toLowerCase()], `duplicate OpenAPI operation ${method} ${path}`)
        operationIds.add(id)
        const [authClass, security] = authFor(path, method)
        const parameters = [...path.matchAll(/{([A-Za-z0-9]+)}/g)].map((match) => ({
          name: match[1],
          in: 'path',
          required: true,
          description: parameterName(match[1]),
          schema: { type: 'string', minLength: 1 },
        }))
        paths[path][method.toLowerCase()] = {
          operationId: id,
          summary: `${method} ${words(route.handler)}`,
          tags: [tagFor(path)],
          security,
          parameters,
          responses: {
            '2XX': { description: 'Successful response. Exact status and body are defined in API.md and handler tests.' },
            default: {
              description: 'Error response.',
              content: { 'application/json': { schema: { $ref: '#/components/schemas/ErrorEnvelope' } } },
            },
          },
          'x-sesame-auth': authClass,
          'x-sesame-availability': availability(path),
          'x-sesame-handler': route.handler,
          'x-sesame-registration-pattern': route.registration,
        }
      }
    }
  }

  assert.equal(covered.size, registrations.length, 'not every mux registration was covered')
  assert.deepEqual(
    Object.keys(dynamicRoutes).filter((pattern) => !covered.has(pattern)),
    [],
    'dynamic OpenAPI expansion names an unregistered route',
  )
  return {
    openapi: '3.1.1',
    jsonSchemaDialect: 'https://json-schema.org/draft/2020-12/schema',
    info: {
      title: 'Sesame vault-blind service API',
      version: '0.1.0',
      description: 'Generated endpoint, method, authentication, availability, and ownership contract. API.md remains the detailed closed request and response schema reference.',
      license: { name: 'AGPL-3.0-or-later', identifier: 'AGPL-3.0-or-later' },
    },
    servers: [{ url: 'https://api.example.invalid', description: 'Deployment-owned API origin placeholder.' }],
    tags: [
      ['health', 'Process and database health.'],
      ['public', 'Vault-blind public metadata.'],
      ['account', 'Optional browser account and support portal.'],
      ['desktop', 'Linked desktop account connection.'],
      ['release-pipeline', 'Protected release pipeline ingestion.'],
      ['admin', 'Separate-origin administrator control plane.'],
      ['sync-disabled', 'Built-disabled ciphertext-only Sync preview.'],
    ].map(([name, description]) => ({ name, description })),
    paths: Object.fromEntries(Object.entries(paths).sort(([left], [right]) => left.localeCompare(right))),
    components: {
      securitySchemes: {
        browserSession: { type: 'apiKey', in: 'cookie', name: 'sesame_session' },
        browserCsrfCookie: { type: 'apiKey', in: 'cookie', name: 'sesame_csrf' },
        browserCsrfHeader: { type: 'apiKey', in: 'header', name: 'X-Sesame-CSRF' },
        adminSession: { type: 'apiKey', in: 'cookie', name: 'sesame_admin_session' },
        adminCsrfCookie: { type: 'apiKey', in: 'cookie', name: 'sesame_admin_csrf' },
        adminCsrfHeader: { type: 'apiKey', in: 'header', name: 'X-Sesame-CSRF' },
        desktopToken: { type: 'apiKey', in: 'header', name: 'Authorization', description: 'Exact form: Sesame <opaque device token>.' },
        releasePipelineToken: { type: 'apiKey', in: 'header', name: 'Authorization', description: 'Protected release-pipeline bearer credential.' },
      },
      schemas: {
        ErrorEnvelope: {
          type: 'object',
          additionalProperties: false,
          required: ['error'],
          properties: {
            error: {
              type: 'object',
              additionalProperties: false,
              required: ['code', 'message'],
              properties: {
                code: { type: 'string' },
                message: { type: 'string' },
              },
            },
          },
        },
      },
    },
    'x-sesame-generated-from': registrationFiles.map((name) => `internal/httpapi/${name}`),
    'x-sesame-operation-count': operationIds.size,
  }
}

const rendered = `${JSON.stringify(build(), null, 2)}\n`
const mode = process.argv[2]
if (mode === 'generate') {
  mkdirSync(dirname(outputPath), { recursive: true })
  writeFileSync(outputPath, rendered, 'utf8')
  console.log(`OpenAPI generated: ${JSON.parse(rendered)['x-sesame-operation-count']} operations.`)
} else if (mode === 'check') {
  const current = readFileSync(outputPath, 'utf8')
  assert.equal(current, rendered, 'openapi/openapi.json is stale; run npm run openapi:generate')
  console.log(`OpenAPI checked: ${JSON.parse(rendered)['x-sesame-operation-count']} operations.`)
} else {
  console.error('Usage: node scripts/openapi.mjs generate|check')
  process.exitCode = 2
}
