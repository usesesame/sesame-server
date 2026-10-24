import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import test from 'node:test'

const root = dirname(dirname(fileURLToPath(import.meta.url)))
const workflow = readFileSync(join(root, '.github', 'workflows', 'ci.yml'), 'utf8')

function jobBlock(body, job) {
  const start = body.indexOf(`\n  ${job}:\n`)
  assert.ok(start >= 0, `the ${job} job is missing from the workflow`)
  const rest = body.slice(start + 1)
  const afterFirst = rest.indexOf('\n') + 1
  const next = rest.slice(afterFirst).match(/^ {2}[a-z0-9_-]+:$/m)
  return next ? rest.slice(0, afterFirst + next.index) : rest
}

const auditCommands = {
  server: 'node scripts/audit-dependencies.mjs',
  account: 'node ../../scripts/audit-dependencies.mjs',
  admin: 'node ../../scripts/audit-dependencies.mjs',
  selfhost: 'node ../../scripts/audit-dependencies.mjs',
}

for (const [job, auditCommand] of Object.entries(auditCommands)) {
  test(`the ${job} job audits npm dependencies at high through the reviewed exceptions`, () => {
    const commands = [...jobBlock(workflow, job).matchAll(/^\s*(?:- )?run: (.+)$/gm)].map(
      ([, command]) => command.trim(),
    )
    assert.ok(
      commands.includes(auditCommand),
      `the ${job} job must run \`${auditCommand}\` as a step, so a high advisory fails CI`,
    )
    assert.ok(
      !commands.some((command) => command.startsWith('npm audit')),
      `the ${job} job runs a bare npm audit, which ignores the reviewed exceptions`,
    )
  })
}

const npmProjects = [
  { path: '.', directory: '/' },
  { path: 'web/account', directory: '/web/account' },
  { path: 'web/admin', directory: '/web/admin' },
  { path: 'web/console', directory: '/web/console' },
]

for (const { path } of npmProjects) {
  const name = path === '.' ? 'the root package' : path
  test(`${name} waits seven days for a new npm release`, () => {
    const npmrc = readFileSync(join(root, path, '.npmrc'), 'utf8')
    assert.match(
      npmrc,
      /^min-release-age=7$/m,
      `${path}/.npmrc must set min-release-age=7, so npm ignores releases younger than a week`,
    )
    for (const line of ['ignore-scripts=true', 'allow-git=none']) {
      assert.ok(npmrc.split(/\r?\n/).includes(line), `${path}/.npmrc no longer pins ${line}`)
    }
  })
}

test('image builds install npm packages without running their scripts', () => {
  for (const dockerfile of ['Dockerfile.selfhost', 'web/account/Dockerfile', 'web/admin/Dockerfile']) {
    const installs = [...readFileSync(join(root, dockerfile), 'utf8').matchAll(/^RUN npm (ci|install)\b(.*)$/gm)]
    assert.ok(installs.length > 0, `${dockerfile} no longer installs npm packages, so this check is stale`)
    for (const [line, , flags] of installs) {
      assert.match(flags, /--ignore-scripts\b/, `${dockerfile} runs package scripts in the image build: ${line}`)
      assert.match(flags, /--allow-git=none\b/, `${dockerfile} can fetch git dependencies in the image build, because .npmrc is copied after the install: ${line}`)
    }
  }
})

function dependabotEntries(body) {
  const entries = []
  let entry = null
  for (const line of body.split('\n')) {
    const ecosystem = line.match(/^ {2}- package-ecosystem: (.+)$/)
    if (ecosystem) {
      entry = { ecosystem: ecosystem[1].trim(), directory: null, cooldown: {} }
      entries.push(entry)
      continue
    }
    if (!entry) continue
    const directory = line.match(/^ {4}directory: (.+)$/)
    if (directory) entry.directory = directory[1].trim()
    const cooldown = line.match(/^ {6}(default-days|semver-major-days): (\d+)$/)
    if (cooldown) entry.cooldown[cooldown[1]] = Number(cooldown[2])
  }
  return entries
}

test('Dependabot waits seven days for npm and action releases, and fourteen for npm majors', () => {
  const entries = dependabotEntries(
    readFileSync(join(root, '.github', 'dependabot.yml'), 'utf8'),
  )
  for (const { directory } of npmProjects) {
    const entry = entries.find(
      (candidate) => candidate.ecosystem === 'npm' && candidate.directory === directory,
    )
    assert.ok(entry, `Dependabot has no npm entry for ${directory}`)
    assert.equal(
      entry.cooldown['default-days'],
      7,
      `the ${directory} npm entry must wait seven days`,
    )
    assert.equal(
      entry.cooldown['semver-major-days'],
      14,
      `the ${directory} npm entry must wait fourteen days for a major version`,
    )
  }
  const actions = entries.filter((entry) => entry.ecosystem === 'github-actions')
  assert.ok(actions.length >= 1, 'Dependabot has no github-actions entry')
  for (const entry of actions) {
    assert.equal(
      entry.cooldown['default-days'],
      7,
      `the ${entry.directory} github-actions entry must wait seven days`,
    )
  }
})

test('Dependabot waits seven days for Go modules and base images, and fourteen for Go majors', () => {
  const entries = dependabotEntries(
    readFileSync(join(root, '.github', 'dependabot.yml'), 'utf8'),
  )
  const gomod = entries.filter((entry) => entry.ecosystem === 'gomod')
  const docker = entries.filter((entry) => entry.ecosystem === 'docker')
  assert.ok(gomod.length >= 1 && docker.length >= 1, 'Dependabot has no gomod or docker entry')
  for (const entry of gomod) {
    assert.deepEqual(
      entry.cooldown,
      { 'default-days': 7, 'semver-major-days': 14 },
      `the ${entry.directory} gomod entry must wait seven days and fourteen for a major version`,
    )
  }
  for (const entry of docker) {
    assert.equal(entry.cooldown['default-days'], 7, `the ${entry.directory} docker entry must wait seven days`)
  }
})
