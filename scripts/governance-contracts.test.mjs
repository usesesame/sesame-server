import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { existsSync, readFileSync, readdirSync, statSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import test from 'node:test'
import { jobEnvironment, steps as verifySteps } from './verify.mjs'

const root = dirname(dirname(fileURLToPath(import.meta.url)))
const read = (...parts) => readFileSync(join(root, ...parts), 'utf8')

const workflows = readdirSync(join(root, '.github', 'workflows'))
  .filter((name) => /\.(ya?ml)$/.test(name))
  .map((name) => join('.github', 'workflows', name))
  .sort()

const repository = 'usesesame/sesame-server'

const jobBlock = (body, job) => {
  const start = body.indexOf(`\n  ${job}:\n`)
  assert.ok(start >= 0, `the ${job} job is missing from the workflow`)
  const rest = body.slice(start + 1)
  const afterFirst = rest.indexOf('\n') + 1
  const next = rest.slice(afterFirst).match(/^ {2}[a-z0-9_-]+:$/m)
  return next ? rest.slice(0, afterFirst + next.index) : rest
}

test('every workflow declares permissions and pins every third-party action', () => {
  assert.ok(workflows.length >= 2, `expected this repository's workflows, found ${workflows.length}`)

  const missingPermissions = []
  const unpinned = []
  for (const workflow of workflows) {
    const body = read(workflow)
    if (!/^permissions:\s*$/m.test(body)) missingPermissions.push(workflow)
    for (const [, action] of body.matchAll(/uses:\s*([^\s#]+)/g)) {
      if (action.startsWith('./')) continue
      if (action.startsWith(`${repository}/`) && action.length > repository.length + 1 && !action.includes('@')) continue
      if (!/@[0-9a-f]{40}$/.test(action)) unpinned.push(`${workflow}: ${action}`)
    }
  }
  assert.deepEqual(missingPermissions, [], `these workflows inherit their permissions:\n  ${missingPermissions.join('\n  ')}`)
  assert.deepEqual(unpinned, [], `a moved tag would change what these runs execute:\n  ${unpinned.join('\n  ')}`)
})

test('a workflow that writes says so at the job that writes', () => {
  for (const workflow of workflows) {
    const body = read(workflow)
    const header = body.slice(0, body.indexOf('\njobs:'))
    assert.match(
      header,
      /^permissions:\s*\n\s+contents: read\s*$/m,
      `${workflow} should default to contents: read at the top and widen per job`,
    )
  }
  const release = read('.github', 'workflows', 'release.yml')
  const verifyJob = jobBlock(release, 'verify')
  assert.doesNotMatch(
    verifyJob,
    /^\s+id-token:/m,
    'the verify job builds dependencies and must not mint an OIDC token',
  )
  assert.doesNotMatch(
    verifyJob,
    /^\s+[a-z][a-z0-9-]*:\s*write\s*$/m,
    'the verify job must not hold a write permission',
  )
  const imagesJob = jobBlock(release, 'images')
  assert.doesNotMatch(
    imagesJob,
    /^\s+id-token:/m,
    'the images job builds images and must not mint an OIDC token',
  )
  assert.doesNotMatch(
    imagesJob,
    /^\s+[a-z][a-z0-9-]*:\s*write\s*$/m,
    'the images job must not hold a write permission',
  )
  const publishJob = jobBlock(release, 'publish')
  assert.match(
    publishJob,
    /^\s+environment:\s*server-release\s*$/m,
    'the publish job should run behind its protected environment',
  )
  assert.match(
    publishJob,
    /^\s+id-token:\s*write\s*$/m,
    'the publish job signs and attests keylessly and needs an OIDC token',
  )
  assert.doesNotMatch(
    publishJob,
    /\bnpm ci\b/,
    'the publish job publishes prebuilt artifacts and must not install dependencies',
  )
  assert.doesNotMatch(
    publishJob,
    /\bgo\b/,
    'the publish job publishes prebuilt artifacts and must not run the Go toolchain',
  )
  assert.doesNotMatch(
    publishJob,
    /\bdocker build\b/,
    'the publish job publishes prebuilt artifacts and must not build images',
  )
})

test('the release verifies the toolchain tests in every web app', () => {
  const verify = jobBlock(read('.github', 'workflows', 'release.yml'), 'verify')
  for (const app of ['account', 'admin', 'console']) {
    const manifest = JSON.parse(read('web', app, 'package.json'))
    const commands = manifest.scripts['release:check'].split('&&').map((command) => command.trim())
    assert.ok(commands.includes('npm run check'), `${app} type checks must gate releases`)
    assert.match(verify, new RegExp(`^ {6}- run: npm run release:check\\r?\\n {8}working-directory: web/${app}[ \\t]*$`, 'm'))
    if (!commands.includes('npm test') && !commands.includes('npm run test')) {
      assert.match(verify, new RegExp(`^ {6}- run: npm test\\r?\\n {8}working-directory: web/${app}[ \\t]*$`, 'm'), `${app} toolchain tests must gate releases`)
    }
  }
})

test('the release restores no cache that another run could have written', () => {
  const release = read('.github', 'workflows', 'release.yml')
  assert.doesNotMatch(release, /^\s+cache:(?!\s*false\s*$)/m, 'the release restores a package or build cache')
  assert.doesNotMatch(release, /cache-dependency-path/, 'the release keys a cache on a lockfile')
  const setups = release.split('uses: actions/setup-go@').slice(1).map((rest) => rest.split(/\n\s*\n/)[0])
  for (const setup of setups) {
    assert.match(setup, /^\s+cache: false\s*$/m, 'setup-go caches modules and builds unless the release turns it off')
  }
})

test('the publish job verifies the artifact digests the images job records', () => {
  const release = read('.github', 'workflows', 'release.yml')
  const imagesJob = jobBlock(release, 'images')
  const publishJob = jobBlock(release, 'publish')
  for (const output of ['images_sha256', 'api_sbom_sha256', 'account_sbom_sha256', 'admin_sbom_sha256']) {
    assert.match(
      imagesJob,
      new RegExp(`^\\s+${output}:\\s*\\$\\{\\{ steps\\.[a-z0-9_-]+\\.outputs\\.${output} \\}\\}\\s*$`, 'm'),
      `the images job does not expose ${output}`,
    )
    assert.ok(
      publishJob.includes(`needs.images.outputs.${output}`),
      `the publish job does not read ${output} from the images job`,
    )
  }
  const hashed = publishJob.indexOf('sha256sum')
  assert.ok(hashed > 0, 'the publish job does not hash the downloaded artifacts')
  assert.ok(
    publishJob.indexOf('docker load') > hashed,
    'the publish job loads the artifacts before it verifies their digests',
  )
  assert.ok(
    publishJob.indexOf('docker push') > hashed,
    'the publish job pushes artifacts before it verifies their digests',
  )
})

test('every job a workflow depends on exists in that workflow', () => {
  for (const workflow of workflows) {
    const body = read(workflow)
    const names = new Set([...body.matchAll(/^ {2}([a-z0-9_-]+):$/gm)].map(([, name]) => name))
    for (const [, list] of body.matchAll(/^\s+needs:\s*(.+)$/gm)) {
      for (const name of list.replaceAll('[', ' ').replaceAll(']', ' ').split(',')) {
        const job = name.trim()
        if (!job) continue
        assert.ok(names.has(job), `${workflow} requires job ${job}, which the workflow does not define`)
      }
    }
  }
})

test('review routing names paths that exist in this repository', () => {
  const owners = read('.github', 'CODEOWNERS')
  assert.match(owners, /^\*\s+@/m, 'the repository has no default owner')
  for (const control of ['/.github/', '/package.json']) {
    assert.ok(owners.includes(control), `the repository does not route ${control}`)
  }
  for (const control of ['/package-lock.json', '/go.sum']) {
    assert.ok(owners.includes(control), `the repository does not route ${control}`)
  }
  for (const [, routed] of owners.matchAll(/^(\/[^\s#]+)/gm)) {
    assert.ok(
      existsSync(join(root, routed.slice(1))),
      `CODEOWNERS routes ${routed}, which does not exist in this repository`,
    )
  }
})

test('every dependency ecosystem this repository uses is updated', () => {
  const body = read('.github', 'dependabot.yml')
  for (const ecosystem of ['gomod', 'npm', 'docker', 'github-actions']) {
    assert.match(body, new RegExp(`package-ecosystem: ${ecosystem}\\b`), `dependabot does not update ${ecosystem}`)
  }
})

test('the security policy tells a reporter where to send a vulnerability', () => {
  assert.ok(statSync(join(root, 'SECURITY.md')).isFile())
  const body = read('SECURITY.md')
  assert.match(body, /Do not open a public issue/i, 'the policy does not say to report privately')
  assert.match(body, /Report a vulnerability/, 'the policy does not name the private reporting route')
  assert.match(body, /## Scope/, 'the policy has no scope, so a reporter cannot tell what counts')
  assert.match(body, /vault-blind/i, 'the policy is not scoped to this product')
})

const verifiedJobs = ['server', 'account', 'admin', 'selfhost']

function workflowRunSteps(job) {
  const block = jobBlock(read('.github', 'workflows', 'ci.yml'), job)
  const jobDirectory = block.match(/^ {4}defaults:\n {6}run:\n {8}working-directory: (.+)$/m)?.[1].trim() ?? '.'
  return block
    .split(/\n(?= {6}- )/)
    .slice(1)
    .map((step) => ({
      job,
      directory: step.match(/^ {8}working-directory: (.+)$/m)?.[1].trim() ?? jobDirectory,
      command: step.match(/^ {6}(?:- | {2})run: ([^|\n].*)$/m)?.[1].trim(),
    }))
    .filter((step) => step.command)
}

test('the verify command runs the non Docker CI jobs in workflow order', () => {
  const ciSteps = verifiedJobs.flatMap(workflowRunSteps)
  assert.deepEqual(
    verifySteps.map(({ job, directory, command }) => ({ job, directory, command: command.join(' ') })),
    ciSteps,
    'scripts/verify.mjs and the server, account, admin and selfhost jobs in ci.yml must list the same commands in the same order',
  )
  assert.equal(JSON.parse(read('package.json')).scripts.verify, 'node ./scripts/verify.mjs')
})

test('the verify command sets the environment the portal jobs set', () => {
  const workflow = read('.github', 'workflows', 'ci.yml')
  for (const job of ['account', 'admin']) {
    const lines = jobBlock(workflow, job).match(/^ {4}env:\n((?: {6}.+\n)+)/m)?.[1] ?? ''
    const environment = Object.fromEntries([...lines.matchAll(/^ {6}(\w+): (.+)$/gm)].map(([, name, value]) => [name, value.trim()]))
    assert.deepEqual(jobEnvironment[job], environment, `verify must set the ${job} job environment`)
  }
  const required = verifySteps.find((step) => step.command.join(' ') === 'npm run backup:contract')
  assert.deepEqual(required.environment, { SESAME_REQUIRE_AGE: '1' })
})

test('the verify command refuses to run without a test database', () => {
  const environment = { ...process.env }
  delete environment.SESAME_TEST_DATABASE_URL
  const result = spawnSync(process.execPath, [join(root, 'scripts', 'verify.mjs')], { env: environment, encoding: 'utf8' })
  assert.equal(result.status, 1)
  assert.match(result.stderr, /SESAME_TEST_DATABASE_URL/)
})

test('the server job leaves the plain Go test run to the race run', () => {
  const scripts = JSON.parse(read('package.json')).scripts
  assert.equal(scripts['test:race'], scripts.test.replace('test ./...', 'test -race ./...'), 'the race run must cover the same packages and flags as the plain run')
  assert.equal(scripts.test, 'node ./scripts/run-go.mjs test ./...')
  assert.equal(scripts.ci, 'npm run ci:checks && npm run test && npm run test:fuzz', 'the local command keeps the plain run')
  const commands = workflowRunSteps('server').map((step) => step.command)
  assert.deepEqual(commands, ['npm run ci:checks', 'npm run test:fuzz', 'npm run test:race', 'npm run hosted:smoke', 'npm run hosted:upgrade', 'npm run vuln:check'])
  assert.doesNotMatch(scripts['ci:checks'], /npm run test(?![:\w])/, 'ci:checks must not run the plain Go tests')
})

test('the hosted checks run after the Go tests, fetch the tags they need and cannot skip', () => {
  const scripts = JSON.parse(read('package.json')).scripts
  assert.equal(scripts['hosted:smoke'], 'node ./scripts/hosted-smoke.mjs')
  assert.equal(scripts['hosted:upgrade'], 'node ./scripts/hosted-upgrade-check.mjs')
  assert.equal(scripts['hosted:contract'], 'node --test scripts/hosted-stack.test.mjs')
  assert.ok(scripts['ci:checks'].includes('npm run hosted:contract'), 'ci:checks must run the hosted helper tests')
  assert.doesNotMatch(scripts['ci:checks'], /hosted:(smoke|upgrade)/, 'the hosted journeys need the database, so they run after the Go tests')
  const server = jobBlock(read('.github', 'workflows', 'ci.yml'), 'server')
  assert.match(server, /actions\/checkout@[0-9a-f]{40} # v[0-9.]+\n {8}with:\n {10}fetch-depth: 0\n/, 'the upgrade check needs every release tag in the checkout')
  const commands = workflowRunSteps('server').map((step) => step.command)
  assert.ok(commands.indexOf('npm run test:race') < commands.indexOf('npm run hosted:smoke'), 'the hosted journey must run after the Go tests')
  assert.equal(commands.indexOf('npm run hosted:smoke') + 1, commands.indexOf('npm run hosted:upgrade'))
  const environment = { ...process.env }
  delete environment.SESAME_TEST_DATABASE_URL
  for (const script of ['hosted-smoke.mjs', 'hosted-upgrade-check.mjs']) {
    const result = spawnSync(process.execPath, [join(root, 'scripts', script)], { env: environment, encoding: 'utf8' })
    assert.equal(result.status, 1, `${script} must fail without a database, not skip`)
    assert.match(result.stderr, /SESAME_TEST_DATABASE_URL/)
    assert.doesNotMatch(read('scripts', script), /skip/i, `${script} must not hold a skip path`)
  }
})

test('every dependency update entry groups minor and patch updates and leaves major updates alone', () => {
  const entries = read('.github', 'dependabot.yml').split(/^ {2}- (?=package-ecosystem:)/m).slice(1)
  assert.equal(entries.length, 10, 'this check is stale: the number of update entries changed')
  for (const entry of entries) {
    const name = entry.split('\n', 1)[0]
    assert.match(
      entry,
      /^ {4}groups:\n {6}minor-and-patch:\n {8}update-types:\n {10}- minor\n {10}- patch\n(?! {10}- )/m,
      `${name} must group its minor and patch updates and nothing else`,
    )
    assert.doesNotMatch(entry, /- major\b/, `${name} must keep major updates in their own pull requests`)
    assert.match(entry, /^ {4}schedule:\n {6}interval: weekly$/m, `${name} lost its weekly schedule`)
    assert.match(entry, /^ {4}cooldown:\n {6}default-days: 7$/m, `${name} lost its cooldown`)
  }
})
