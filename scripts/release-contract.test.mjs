import assert from 'node:assert/strict'
import { chmodSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'
import { spawnSync } from 'node:child_process'
import { assertCiPassed, digestReference, releaseBuildConfig, releaseIdentity } from './release-contract.mjs'

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
  for (const file of ['../Dockerfile', '../Dockerfile.selfhost', '../web/account/Dockerfile', '../web/admin/Dockerfile']) {
    const dockerfile = readFileSync(new URL(file, import.meta.url), 'utf8')
    const bases = [...dockerfile.matchAll(/^FROM (?:--platform=\S+ )?([^\s]+)(?: AS \w+)?$/gm)].map(match => match[1])
    assert.ok(bases.length > 0)
    for (const base of bases) assert.match(base, /@sha256:[0-9a-f]{64}$/)
  }
  for (const file of ['../.github/workflows/release.yml', '../deploy/compose/compose.release-smoke.yaml']) {
    const config = readFileSync(new URL(file, import.meta.url), 'utf8')
    assert.match(config, /image: postgres:18-alpine@sha256:[0-9a-f]{64}/)
  }
})

test('publishes the self-host image for two platforms only after verification and a container check', () => {
  const workflow = readFileSync(new URL('../.github/workflows/release.yml', import.meta.url), 'utf8')
  const start = workflow.indexOf('\n  selfhost:\n')
  assert.ok(start > 0, 'the release workflow has no selfhost job')
  const job = workflow.slice(start)
  assert.match(job, /needs: verify\n/)
  assert.match(job, /environment: server-release/)
  assert.match(job, /run: node scripts\/build-selfhost-images\.mjs\n/)
  const builder = readFileSync(new URL('./build-selfhost-images.mjs', import.meta.url), 'utf8')
  assert.match(builder, /'linux\/amd64'/)
  assert.match(builder, /'linux\/arm64'/)
  assert.match(job, /--arch amd64 .* --run\n/)
  assert.match(job, /--arch arm64 .* --run\n/)
  assert.match(job, /docker\/setup-qemu-action@[0-9a-f]{40}/)
  const gates = ['npm run release:check', 'npm run selfhost:smoke']
  for (const gate of gates) {
    const position = workflow.indexOf(gate)
    assert.ok(position > 0 && position < start, `${gate} must run in the verify job`)
  }
  const check = job.indexOf('Check the images before they are published')
  const login = job.indexOf('docker/login-action')
  const push = job.indexOf('docker push "$SELFHOST_AMD64_IMAGE"')
  assert.ok(check > 0 && check < login && login < push, 'the images must be checked before login and push')
  assert.equal(job.match(/uses: actions\/attest@/g)?.length, 3)
  assert.equal(job.match(/sbom-path: selfhost-(?:amd64|arm64)\.spdx\.json/g)?.length, 2)
  assert.match(job, /subject-digest: \$\{\{ steps\.push\.outputs\.index_digest \}\}/)
})

const taggedCommit = 'a'.repeat(40)
const passingRun = { id: 101, status: 'completed', conclusion: 'success', head_sha: taggedCommit, event: 'push' }
const passingJobs = [
  { name: 'server', status: 'completed', conclusion: 'success' },
  { name: 'account', status: 'completed', conclusion: 'success' },
]

test('accepts a tagged commit whose Server CI run passed every job', () => {
  assert.doesNotThrow(() => assertCiPassed(taggedCommit, [passingRun], { 101: passingJobs }))
})

test('fails closed and asks for a rerun when Server CI has not run or has not finished', () => {
  assert.throws(() => assertCiPassed(taggedCommit, [], {}), /wait for CI to start.*rerun this workflow/i)
  assert.throws(
    () => assertCiPassed(taggedCommit, [{ ...passingRun, status: 'in_progress', conclusion: null }], { 101: passingJobs }),
    /still running.*rerun this workflow/i,
  )
  assert.throws(
    () => assertCiPassed(taggedCommit, [passingRun], { 101: [...passingJobs, { name: 'admin', status: 'queued', conclusion: null }] }),
    /still running.*rerun this workflow/i,
  )
})

test('refuses a tagged commit whose Server CI run failed, was cancelled, skipped a job or reported no jobs', () => {
  for (const conclusion of ['failure', 'cancelled', 'skipped', 'timed_out', 'neutral', null]) {
    assert.throws(
      () => assertCiPassed(taggedCommit, [passingRun], { 101: [...passingJobs, { name: 'admin', status: 'completed', conclusion }] }),
      /did not pass.*admin/,
    )
    assert.throws(() => assertCiPassed(taggedCommit, [{ ...passingRun, conclusion }], { 101: passingJobs }), /did not pass/)
  }
  assert.throws(() => assertCiPassed(taggedCommit, [passingRun], { 101: [] }), /reports no jobs/)
  assert.throws(() => assertCiPassed(taggedCommit, [passingRun], {}), /reports no jobs/)
})

test('refuses a tagged commit when any one of several Server CI runs did not pass', () => {
  const failingRun = { ...passingRun, id: 102, conclusion: 'failure' }
  const failingJobs = [{ name: 'server', status: 'completed', conclusion: 'failure' }]
  assert.throws(() => assertCiPassed(taggedCommit, [passingRun, failingRun], { 101: passingJobs, 102: failingJobs }), /did not pass.*server failure/)
})

test('rejects a malformed commit before reading CI', () => {
  assert.throws(() => assertCiPassed('main', [passingRun], { 101: passingJobs }), /40-character/)
})

function runCiCommand(responses, { token = 'fictional-token', repository = 'example/sesame-fixture' } = {}) {
  const directory = mkdtempSync(join(tmpdir(), 'sesame-release-ci-'))
  const calls = join(directory, 'calls.txt')
  const stub = join(directory, 'gh')
  writeFileSync(stub, `#!/bin/sh
printf '%s\\n' "$*" >> '${calls}'
case "$*" in
  *"/jobs?"*) cat '${join(directory, 'jobs.json')}' ;;
  *) cat '${join(directory, 'runs.json')}' ;;
esac
`)
  chmodSync(stub, 0o755)
  writeFileSync(join(directory, 'runs.json'), JSON.stringify(responses.runs))
  writeFileSync(join(directory, 'jobs.json'), JSON.stringify(responses.jobs))
  const environment = { ...process.env, PATH: `${directory}:${process.env.PATH}` }
  delete environment.GH_TOKEN
  if (token) environment.GH_TOKEN = token
  const script = new URL('./release-contract.mjs', import.meta.url).pathname
  return { ...spawnSync(process.execPath, [script, 'ci', repository, taggedCommit], { env: environment, encoding: 'utf8' }), calls: () => readFileSync(calls, 'utf8') }
}

test('reads the Server CI run of the tagged commit through the GitHub CLI', () => {
  const passing = runCiCommand({ runs: [{ workflow_runs: [passingRun] }], jobs: [{ jobs: passingJobs }] })
  assert.equal(passing.status, 0, passing.stderr)
  assert.match(passing.calls(), /api --paginate --slurp repos\/example\/sesame-fixture\/actions\/workflows\/ci\.yml\/runs\?head_sha=a{40}&event=push/)
  assert.match(passing.calls(), /repos\/example\/sesame-fixture\/actions\/runs\/101\/jobs/)

  const failing = runCiCommand({ runs: [{ workflow_runs: [passingRun] }], jobs: [{ jobs: [{ name: 'server', status: 'completed', conclusion: 'failure' }] }] })
  assert.equal(failing.status, 1)
  assert.match(failing.stderr, /did not pass/)

  const missing = runCiCommand({ runs: [{ workflow_runs: [] }], jobs: [] })
  assert.equal(missing.status, 1)
  assert.match(missing.stderr, /rerun this workflow/)
})

test('ignores runs that belong to another commit or event and refuses without a token', () => {
  const other = runCiCommand({
    runs: [{ workflow_runs: [{ ...passingRun, head_sha: 'b'.repeat(40) }, { ...passingRun, event: 'pull_request' }] }],
    jobs: [{ jobs: passingJobs }],
  })
  assert.equal(other.status, 1)
  assert.match(other.stderr, /No Server CI run exists/)

  const anonymous = runCiCommand({ runs: [{ workflow_runs: [passingRun] }], jobs: [{ jobs: passingJobs }] }, { token: '' })
  assert.equal(anonymous.status, 1)
  assert.match(anonymous.stderr, /GH_TOKEN is required/)
})

test('requires a passing Server CI run before the verify job does any other work', () => {
  const workflow = readFileSync(new URL('../.github/workflows/release.yml', import.meta.url), 'utf8')
  const start = workflow.indexOf('\n  verify:\n')
  const verify = workflow.slice(start, workflow.indexOf('\n  images:\n'))
  assert.match(verify, /^ {4}permissions:\n {6}actions: read\n {6}contents: read\n/m)
  assert.doesNotMatch(verify, /^\s+(?:checks|[a-z-]+): write$/m)
  const gate = verify.indexOf('run: node scripts/release-contract.mjs ci "$GITHUB_REPOSITORY" "$GITHUB_SHA"')
  assert.ok(gate > 0, 'the verify job does not check the CI run of the tagged commit')
  assert.match(verify.slice(Math.max(0, gate - 120), gate), /GH_TOKEN: \$\{\{ github\.token \}\}\n\s+$/)
  for (const work of ['id: contract', 'is-ancestor', 'npm run ci', 'npm run test:race', 'npm run vuln:check', 'npm ci', 'npm run release:check', 'selfhost:smoke']) {
    assert.ok(verify.indexOf(work) > gate, `${work} must run after the CI run check`)
  }
})
