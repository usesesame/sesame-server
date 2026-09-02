import assert from 'node:assert/strict'
import { mkdtempSync, readFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'
import { digestReference, releaseBuildConfig, releaseIdentity } from './release-contract.mjs'

test('normalizes one release identity', () => {
  assert.deepEqual(releaseIdentity('v1.2.3', '0123456789ABCDEF0123456789ABCDEF01234567'), {
    version: '1.2.3',
    commit: '0123456789abcdef0123456789abcdef01234567',
  })
})

test('rejects malformed and partial identities', () => {
  for (const version of ['', '1.2', 'v01.2.3', 'latest', '1.2.3+local', '1.2.3-01']) {
    assert.throws(() => releaseIdentity(version, '0'.repeat(40)))
  }
  for (const commit of ['', 'abc123', 'g'.repeat(40), '0'.repeat(39)]) {
    assert.throws(() => releaseIdentity('1.2.3', commit))
  }
})

test('accepts only digest-addressed images', () => {
  const reference = `registry.example.invalid/sesame-api@sha256:${'a'.repeat(64)}`
  assert.deepEqual(digestReference(reference, 'API'), {
    reference,
    name: 'registry.example.invalid/sesame-api',
    digest: `sha256:${'a'.repeat(64)}`,
  })
  assert.throws(() => digestReference('registry.example.invalid/sesame-api:1.2.3', 'API'))
})

test('validates every release build input', () => {
  const input = {
    version: '1.2.3',
    commit: '0'.repeat(40),
    apiOrigin: 'https://api.test.invalid',
    siteOrigin: 'https://website.test.invalid',
    sourceURL: 'https://github.com/example/sesame-server',
    capabilityPublicKey: 'A'.repeat(43),
  }
  assert.deepEqual(releaseBuildConfig(input), input)
  assert.throws(() => releaseBuildConfig({ ...input, apiOrigin: 'http://api.test.invalid' }))
  assert.throws(() => releaseBuildConfig({ ...input, siteOrigin: 'https://website.test.invalid/path' }))
  assert.throws(() => releaseBuildConfig({ ...input, sourceURL: 'https://user@example.invalid/repository' }))
  assert.throws(() => releaseBuildConfig({ ...input, capabilityPublicKey: 'not-a-key' }))
})

test('writes bounded portal release metadata', async () => {
  const directory = mkdtempSync(join(tmpdir(), 'sesame-release-contract-'))
  const output = join(directory, 'release.json')
  const { spawnSync } = await import('node:child_process')
  const result = spawnSync(process.execPath, [new URL('./release-contract.mjs', import.meta.url).pathname, 'portal', 'admin', '1.2.3', '0'.repeat(40), output])
  assert.equal(result.status, 0, result.stderr.toString())
  assert.deepEqual(JSON.parse(readFileSync(output, 'utf8')), {
    schemaVersion: 1,
    component: 'admin',
    version: '1.2.3',
    commit: '0'.repeat(40),
  })
})

test('writes one digest-addressed image set', async () => {
  const directory = mkdtempSync(join(tmpdir(), 'sesame-release-manifest-'))
  const output = join(directory, 'server-release.json')
  const script = new URL('./release-contract.mjs', import.meta.url).pathname
  const digest = `sha256:${'b'.repeat(64)}`
  const references = ['api', 'account', 'admin'].map(component => `registry.example.invalid/sesame-${component}@${digest}`)
  const { spawnSync } = await import('node:child_process')
  const result = spawnSync(process.execPath, [script, 'manifest', '1.2.3', '0'.repeat(40), ...references, output])
  assert.equal(result.status, 0, result.stderr.toString())
  const manifest = JSON.parse(readFileSync(output, 'utf8'))
  assert.equal(manifest.version, '1.2.3')
  assert.equal(manifest.commit, '0'.repeat(40))
  assert.deepEqual(Object.values(manifest.images).map(image => image.reference), references)
})

test('gates publication and pins release actions', () => {
  const workflow = readFileSync(new URL('../.github/workflows/release.yml', import.meta.url), 'utf8')
  assert.match(workflow, /environment: server-release/)
  assert.match(workflow, /tags:\s+\- "v\*"/)
  const publication = workflow.indexOf('docker push "$SESAME_API_IMAGE"')
  assert.ok(publication > 0)
  for (const gate of ['npm run ci', 'npm run test:race', 'npm run vuln:check', 'npm run release:check', 'npm run compose:config', 'npm run release:images:build', 'npm run compose:smoke']) {
    assert.ok(workflow.indexOf(gate) > 0 && workflow.indexOf(gate) < publication, `${gate} must run before publication`)
  }
  assert.equal(workflow.match(/npm run release:images:build/g)?.length, 1)
  const actionReferences = [...workflow.matchAll(/^\s*- uses: ([^\s]+)$/gm)].map(match => match[1])
  assert.ok(actionReferences.length > 0)
  for (const reference of actionReferences) assert.match(reference, /@[0-9a-f]{40}$/)
})

test('pins release container inputs', () => {
  for (const file of ['../Dockerfile', '../web/account/Dockerfile', '../web/admin/Dockerfile']) {
    const dockerfile = readFileSync(new URL(file, import.meta.url), 'utf8')
    const bases = [...dockerfile.matchAll(/^FROM ([^\s]+)(?: AS \w+)?$/gm)].map(match => match[1])
    assert.ok(bases.length > 0)
    for (const base of bases) assert.match(base, /@sha256:[0-9a-f]{64}$/)
  }
  for (const file of ['../.github/workflows/release.yml', '../deploy/compose/compose.release-smoke.yaml']) {
    const config = readFileSync(new URL(file, import.meta.url), 'utf8')
    assert.match(config, /image: postgres:18-alpine@sha256:[0-9a-f]{64}/)
  }
})
