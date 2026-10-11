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

const auditCommand = 'npm audit --audit-level=high'

for (const job of ['server', 'account', 'admin']) {
  test(`the ${job} job audits npm dependencies at high`, () => {
    const commands = [...jobBlock(workflow, job).matchAll(/^\s*(?:- )?run: (.+)$/gm)].map(
      ([, command]) => command.trim(),
    )
    assert.ok(
      commands.includes(auditCommand),
      `the ${job} job must run \`${auditCommand}\` as a step, so a high advisory fails CI`,
    )
  })
}

const npmProjects = [
  { path: '.', directory: '/' },
  { path: 'web/account', directory: '/web/account' },
  { path: 'web/admin', directory: '/web/admin' },
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
  })
}

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
