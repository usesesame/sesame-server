import assert from 'node:assert/strict'
import { existsSync, readFileSync, readdirSync } from 'node:fs'
import { dirname, join, relative, sep } from 'node:path'
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

function workflowSteps(job, block) {
  const jobDirectory = block.match(/^ {4}defaults:\n {6}run:\n {8}working-directory: (.+)$/m)?.[1].trim() ?? '.'
  return block.split(/\n(?= {6}- )/).slice(1).map((step) => ({
    job,
    run: step.match(/^ {6}(?:- | {2})run: (.+)$/m)?.[1].trim(),
    directory: step.match(/^ {8}working-directory: (.+)$/m)?.[1].trim() ?? jobDirectory,
  }))
}

const ciJobs = [...workflow.matchAll(/^ {2}([a-z0-9_-]+):$/gm)].map(([, job]) => job)
const ciSteps = ciJobs.flatMap((job) => workflowSteps(job, jobBlock(workflow, job)))

const skippedDirectories = new Set(['node_modules', '.git', 'dist', 'build', 'vendor'])

function lockfileDirectories(directory = '.') {
  const found = existsSync(join(root, directory, 'package-lock.json')) ? [directory] : []
  for (const entry of readdirSync(join(root, directory), { withFileTypes: true })) {
    if (!entry.isDirectory() || skippedDirectories.has(entry.name)) continue
    found.push(...lockfileDirectories(directory === '.' ? entry.name : `${directory}/${entry.name}`))
  }
  return found
}

const lockfileProjects = lockfileDirectories()

function hasDependencies(project) {
  const lockfile = JSON.parse(readFileSync(join(root, project, 'package-lock.json'), 'utf8'))
  const manifest = lockfile.packages?.[''] ?? {}
  return ['dependencies', 'devDependencies', 'optionalDependencies'].some(
    (field) => Object.keys(manifest[field] ?? {}).length > 0,
  )
}

const auditedProjects = lockfileProjects.filter(hasDependencies)

test('the audit contract finds the portal and console lockfiles', () => {
  for (const project of ['web/account', 'web/admin', 'web/console']) {
    assert.ok(auditedProjects.includes(project), `${project} no longer has a lockfile with dependencies, so this check is stale`)
  }
})

for (const project of auditedProjects) {
  const auditCommand = `node ${relative(join(root, project), join(root, 'scripts', 'audit-dependencies.mjs')).split(sep).join('/')}`
  test(`CI audits the npm dependencies of ${project === '.' ? 'the root package' : project} at high through the reviewed exceptions`, () => {
    assert.ok(
      ciSteps.some((step) => step.directory === project && step.run === auditCommand),
      `a CI job must run \`${auditCommand}\` in ${project}, so a high advisory in its lockfile fails CI`,
    )
  })
}

test('no CI job runs a bare npm audit, which ignores the reviewed exceptions', () => {
  for (const step of ciSteps) {
    assert.ok(!step.run?.startsWith('npm audit'), `the ${step.job} job runs a bare npm audit`)
  }
})

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

const portalBuilds = {
  account: { directory: 'web/account', command: 'npm run build' },
  admin: { directory: 'web/admin', command: 'npm run build' },
  selfhost: { directory: 'web/console', command: 'npm run release:check' },
}

for (const [job, build] of Object.entries(portalBuilds)) {
  test(`the ${job} job builds its portal before it audits`, () => {
    const auditCommand = 'node ../../scripts/audit-dependencies.mjs'
    const runs = ciSteps.filter((step) => step.job === job && step.directory === build.directory).map((step) => step.run)
    assert.ok(
      runs.indexOf(build.command) >= 0 && runs.indexOf(build.command) < runs.indexOf(auditCommand),
      `the ${job} job must run \`${build.command}\` before the audit, so the audit knows which packages the portal bundles`,
    )
    const manifest = JSON.parse(readFileSync(join(root, build.directory, 'package.json'), 'utf8'))
    assert.deepEqual(manifest.shippedBuilds, ['portal'], `${build.directory} no longer names its shipped build`)
    assert.match(manifest.scripts.build, /vite build --config vite\.config\.ts/, `${build.directory} no longer builds with the config that records its bundle`)
    assert.match(
      readFileSync(join(root, build.directory, 'vite.config.ts'), 'utf8'),
      /shippedPackages\('portal'\)/,
      `${build.directory} no longer records which packages its build bundles`,
    )
  })
}
